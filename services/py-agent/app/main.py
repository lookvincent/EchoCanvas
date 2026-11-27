"""FastAPI application entrypoint for EchoCanvas Python AI Sidecar."""

from __future__ import annotations

import json
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
from app.schemas import AssetCard, ParseStoryRequest, ParseStoryResponse
from app.security import get_trace_id, verify_internal_token

logger = logging.getLogger("echocanvas.py_agent")


def _persist_gate1_to_pg(
    pool: Any,
    project_id: str | None,
    job_id: str,
    assets: list[AssetCard],
) -> list[AssetCard]:
    """Persist extracted assets (preserving user_ground_truth) and move job to GATE_1."""
    if pool is None:
        return assets

    saved_assets: list[AssetCard] = []
    with pool.connection() as conn:
        with conn.transaction():
            if project_id:
                for asset in assets:
                    row = conn.execute(
                        """
                        INSERT INTO asset_style_profiles (
                            project_id, asset_type, entity_key, display_name,
                            profile_json, reference_image_url, source, is_locked
                        )
                        VALUES (%s, %s, %s, %s, %s::jsonb, %s, %s, %s)
                        ON CONFLICT (project_id, entity_key) DO UPDATE SET
                            asset_type = EXCLUDED.asset_type,
                            display_name = EXCLUDED.display_name,
                            profile_json = EXCLUDED.profile_json,
                            reference_image_url = COALESCE(EXCLUDED.reference_image_url, asset_style_profiles.reference_image_url),
                            source = EXCLUDED.source,
                            is_locked = EXCLUDED.is_locked
                        WHERE asset_style_profiles.source <> 'user_ground_truth'
                           OR EXCLUDED.source = 'user_ground_truth'
                        RETURNING id::text AS id, asset_type, entity_key, display_name,
                                  profile_json, reference_image_url, source, is_locked
                        """,
                        (
                            project_id,
                            asset.asset_type,
                            asset.entity_key,
                            asset.display_name,
                            json.dumps(asset.profile_json, ensure_ascii=False),
                            asset.reference_image_url,
                            asset.source,
                            asset.is_locked,
                        ),
                    ).fetchone()
                    if row is None:
                        row = conn.execute(
                            """
                            SELECT id::text AS id, asset_type, entity_key, display_name,
                                   profile_json, reference_image_url, source, is_locked
                            FROM asset_style_profiles
                            WHERE project_id = %s AND entity_key = %s
                            """,
                            (project_id, asset.entity_key),
                        ).fetchone()
                    saved_assets.append(AssetCard.model_validate(row))
            else:
                saved_assets = assets

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
        active_cp = checkpointer if checkpointer is not None else cp_manager.open()
        callbacks = StoryGraphCallbacks(
            on_assets_extracted=lambda st, assets: _persist_gate1_to_pg(
                cp_manager.pool,
                st.get("project_id"),
                st["job_id"],
                assets,
            )
            if use_db and cp_manager.pool is not None
            else None
        )
        application.state.settings = resolved_settings
        application.state.gateway_client = resolved_gateway
        application.state.cp_manager = cp_manager
        application.state.checkpointer = active_cp
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
        if use_db and cp_manager.pool is not None and payload.project_id:
            with cp_manager.pool.connection() as conn:
                rows = conn.execute(
                    """
                    SELECT id::text AS id, asset_type, entity_key, display_name,
                           profile_json, reference_image_url, source, is_locked
                    FROM asset_style_profiles
                    WHERE project_id = %s
                    ORDER BY created_at ASC
                    """,
                    (payload.project_id,),
                ).fetchall()
                if rows:
                    assets = [AssetCard.model_validate(r) for r in rows]

        return ParseStoryResponse(
            job_id=payload.job_id,
            thread_id=payload.thread_id,
            status="WAITING_GATE",
            current_stage="GATE_1",
            assets=assets,
            trace_id=trace_id,
        )

    return application


app = create_app()
