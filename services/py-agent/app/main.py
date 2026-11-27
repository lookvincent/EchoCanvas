"""FastAPI application entrypoint for EchoCanvas Python AI Sidecar."""

from __future__ import annotations

import logging
from contextlib import asynccontextmanager
from typing import Any

from fastapi import Depends, FastAPI, HTTPException, Request

from app.adapters.gateway_client import (
    BudgetExceededError,
    GatewayClient,
    GatewayError,
    SafetyBlockedError,
    UpstreamUnavailableError,
)
from app.config import Settings
from app.graphs.checkpointer import CheckpointerManager
from app.graphs.story_graph import StoryGraphCallbacks, build_story_graph
from app.memory.repository import InMemoryMemoryRepository, MemoryRepository
from app.memory.working_context import WorkingContextBuilder
from app.schemas import (
    AssetCard,
    MemoryRetrieveRequest,
    MemoryRetrieveResponse,
    ParseStoryRequest,
    ParseStoryResponse,
)
from app.security import get_trace_id, verify_internal_token

logger = logging.getLogger("echocanvas.py_agent")


def _persist_gate1_to_pg(
    pool: Any,
    memory_repo: MemoryRepository | InMemoryMemoryRepository,
    project_id: str | None,
    job_id: str,
    assets: list[AssetCard],
) -> list[AssetCard]:
    """Persist extracted assets with embeddings (preserving user_ground_truth) and move job to GATE_1."""
    saved_assets = (
        memory_repo.upsert_assets(project_id, assets)
        if project_id
        else assets
    )
    if pool is not None:
        with pool.connection() as conn:
            conn.execute(
                """
                UPDATE story_jobs
                SET status = 'WAITING_GATE', current_stage = 'GATE_1'
                WHERE id = %s
                """,
                (job_id,),
            )
    return saved_assets


def create_app(
    settings: Settings | None = None,
    gateway_client: GatewayClient | None = None,
    checkpointer: Any = None,
    enable_db_persistence: bool | None = None,
    memory_repo: Any = None,
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

    @asynccontextmanager
    async def lifespan(application: FastAPI):
        pool = cp_manager.ensure_pool() if use_db else None
        active_cp = checkpointer if checkpointer is not None else cp_manager.open()
        active_mem_repo = memory_repo or (
            MemoryRepository(pool) if pool is not None else InMemoryMemoryRepository()
        )
        callbacks = StoryGraphCallbacks(
            on_assets_extracted=lambda st, assets: _persist_gate1_to_pg(
                pool,
                active_mem_repo,
                st.get("project_id"),
                st["job_id"],
                assets,
            )
            if use_db and pool is not None
            else (
                active_mem_repo.upsert_assets(st["project_id"], assets)
                if st.get("project_id")
                else None
            )
        )
        application.state.settings = resolved_settings
        application.state.gateway_client = resolved_gateway
        application.state.cp_manager = cp_manager
        application.state.db_pool = pool
        application.state.checkpointer = active_cp
        application.state.memory_repo = active_mem_repo
        application.state.context_builder = WorkingContextBuilder(active_mem_repo)
        application.state.story_graph = build_story_graph(
            resolved_gateway,
            active_cp,
            callbacks=callbacks,
        )
        try:
            yield
        finally:
            cp_manager.close()
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

    @application.post(
        "/internal/v1/workflows/parse-story",
        response_model=ParseStoryResponse,
        dependencies=[Depends(verify_internal_token)],
    )
    def parse_story(
        payload: ParseStoryRequest,
        request: Request,
        trace_id: str = Depends(get_trace_id),
    ) -> ParseStoryResponse:
        graph = request.app.state.story_graph
        config = {"configurable": {"thread_id": payload.thread_id}}
        initial_state = {
            "job_id": payload.job_id,
            "project_id": payload.project_id or "",
            "raw_story": payload.raw_story,
            "style_preset": payload.style_preset,
            "aspect_ratio": payload.aspect_ratio,
            "target_frames": payload.target_frames,
            "trace_id": trace_id,
        }
        try:
            result = graph.invoke(initial_state, config=config)
        except BudgetExceededError as exc:
            raise HTTPException(status_code=402, detail=str(exc)) from exc
        except SafetyBlockedError as exc:
            raise HTTPException(status_code=422, detail=str(exc)) from exc
        except UpstreamUnavailableError as exc:
            raise HTTPException(status_code=502, detail=str(exc)) from exc
        except GatewayError as exc:
            raise HTTPException(status_code=exc.status_code, detail=str(exc)) from exc

        assets = [AssetCard.model_validate(item) for item in (result.get("assets") or [])]
        if payload.project_id:
            db_assets = request.app.state.memory_repo.list_project_assets(payload.project_id)
            if db_assets:
                assets = db_assets

        return ParseStoryResponse(
            job_id=payload.job_id,
            thread_id=payload.thread_id,
            status="WAITING_GATE",
            current_stage="GATE_1",
            assets=assets,
            trace_id=trace_id,
        )

    @application.post(
        "/internal/v1/memory/retrieve",
        response_model=MemoryRetrieveResponse,
        dependencies=[Depends(verify_internal_token)],
    )
    def retrieve_memory(payload: MemoryRetrieveRequest, request: Request) -> MemoryRetrieveResponse:
        builder: WorkingContextBuilder = request.app.state.context_builder
        return builder.build(
            project_id=payload.project_id,
            job_id=payload.job_id,
            frame_index=payload.frame_index,
            frame_script=payload.frame_script,
            query_text=payload.query_text,
            style_preset=payload.style_preset,
            max_prompt_chars=payload.max_prompt_chars,
            top_k=payload.top_k,
        )

    return application


app = create_app()
