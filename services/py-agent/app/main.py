"""FastAPI application entrypoint for EchoCanvas Python AI Sidecar."""

from __future__ import annotations

import logging
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI

from app.adapters.gateway_client import GatewayClient
from app.adapters.s3_storage import S3StorageAdapter
from app.adapters.vision_adapter import VisionAdapter
from app.api.routes import router
from app.config import Settings
from app.graphs.checkpointer import CheckpointerManager
from app.graphs.story_graph import build_story_graph
from app.memory.repository import InMemoryMemoryRepository, MemoryRepository
from app.memory.working_context import WorkingContextBuilder
from app.observability.tracing import TraceRecorder
from app.quality.vlm_checker import VLMQualityChecker
from app.repository.story_repo import InMemoryStoryRepository, StoryRepository
from app.services.workflow_service import WorkflowService

logger = logging.getLogger("echocanvas.py_agent")


def create_app(
    settings: Settings | None = None,
    gateway_client: GatewayClient | None = None,
    checkpointer: Any = None,
    enable_db_persistence: bool | None = None,
    memory_repo: Any = None,
    story_repo: Any = None,
    s3_storage: S3StorageAdapter | None = None,
    vision_adapter: VisionAdapter | None = None,
    trace_recorder: TraceRecorder | None = None,
    vlm_checker: VLMQualityChecker | None = None,
) -> FastAPI:
    """Create configured FastAPI app with optional injected dependencies for tests."""
    resolved_settings = settings or Settings.from_env()
    owns_gateway = gateway_client is None
    resolved_gateway = gateway_client or GatewayClient(
        proxy_base_url=resolved_settings.go_gateway_proxy_url,
        internal_token=resolved_settings.internal_auth_token,
        llm_model=resolved_settings.llm_model,
        vision_model=resolved_settings.vision_model,
    )
    use_db = (
        enable_db_persistence
        if enable_db_persistence is not None
        else (checkpointer is None and bool(resolved_settings.database_url))
    )
    cp_manager = CheckpointerManager(resolved_settings.database_url if use_db else "")

    active_s3 = s3_storage
    if (
        active_s3 is None
        and use_db
        and resolved_settings.s3_endpoint
        and resolved_settings.s3_access_key
        and resolved_settings.s3_secret_key
    ):
        active_s3 = S3StorageAdapter(
            endpoint=resolved_settings.s3_endpoint,
            bucket=resolved_settings.s3_bucket,
            access_key=resolved_settings.s3_access_key,
            secret_key=resolved_settings.s3_secret_key,
            region=resolved_settings.s3_region,
            use_ssl=resolved_settings.s3_use_ssl,
            presign_expires_sec=resolved_settings.s3_presign_expires_sec,
        )

    active_vision = vision_adapter or VisionAdapter(
        gateway_client=resolved_gateway,
        s3_storage=active_s3,
        download_remote_images=False,
    )
    owns_tracer = trace_recorder is None
    active_tracer = trace_recorder or TraceRecorder(
        langfuse_host=resolved_settings.langfuse_host,
        public_key=resolved_settings.langfuse_public_key,
        secret_key=resolved_settings.langfuse_secret_key,
    )
    active_vlm_checker = vlm_checker or VLMQualityChecker(
        gateway_client=resolved_gateway,
        pass_threshold=resolved_settings.vlm_pass_threshold,
        max_retries=resolved_settings.vlm_max_retries,
        vlm_model=resolved_settings.vlm_model,
        trace_recorder=active_tracer,
    )

    @asynccontextmanager
    async def lifespan(application: FastAPI):
        pool = cp_manager.ensure_pool() if use_db else None
        active_cp = checkpointer if checkpointer is not None else cp_manager.open()
        active_mem_repo = memory_repo or (
            MemoryRepository(pool) if pool is not None else InMemoryMemoryRepository()
        )
        active_story_repo = story_repo or (
            StoryRepository(pool)
            if pool is not None
            else InMemoryStoryRepository(active_mem_repo)
        )
        context_builder = WorkingContextBuilder(active_mem_repo)
        story_graph = build_story_graph(resolved_gateway, active_cp)
        workflow_service = WorkflowService(
            graph=story_graph,
            gateway_client=resolved_gateway,
            memory_repo=active_mem_repo,
            story_repo=active_story_repo,
            context_builder=context_builder,
            vision_adapter=active_vision,
            vlm_checker=active_vlm_checker,
            trace_recorder=active_tracer,
        )

        application.state.settings = resolved_settings
        application.state.gateway_client = resolved_gateway
        application.state.s3_storage = active_s3
        application.state.vision_adapter = active_vision
        application.state.trace_recorder = active_tracer
        application.state.vlm_checker = active_vlm_checker
        application.state.cp_manager = cp_manager
        application.state.db_pool = pool
        application.state.checkpointer = active_cp
        application.state.memory_repo = active_mem_repo
        application.state.story_repo = active_story_repo
        application.state.context_builder = context_builder
        application.state.story_graph = story_graph
        application.state.workflow_service = workflow_service
        try:
            yield
        finally:
            cp_manager.close()
            if owns_tracer:
                active_tracer.close()
            if owns_gateway:
                resolved_gateway.close()

    application = FastAPI(
        title="EchoCanvas Python AI Sidecar",
        version="0.1.0",
        lifespan=lifespan,
    )
    application.state.settings = resolved_settings

    @application.get("/healthz")
    def healthz() -> dict[str, str]:
        return {"status": "ok", "service": "py-agent"}

    application.include_router(router)
    return application


app = create_app()
