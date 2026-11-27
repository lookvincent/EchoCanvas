"""Unit and live PG16 integration tests for T-M2-01 (FastAPI + LangGraph Gate 1)."""

from __future__ import annotations

import json
import os
import uuid

import httpx
import pytest
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import MemorySaver

from app.adapters.gateway_client import (
    BudgetExceededError,
    GatewayClient,
    SafetyBlockedError,
)
from app.config import Settings
from app.main import create_app
from app.schemas import ExtractedAssetsPayload


def _test_settings(database_url: str = "") -> Settings:
    return Settings(
        database_url=database_url,
        go_gateway_proxy_url="http://mock-gateway.local/proxy/v1",
        internal_auth_token="test_internal_token",
    )


def _sample_assets_json() -> str:
    return json.dumps(
        {
            "assets": [
                {
                    "asset_type": "CHARACTER",
                    "entity_key": "hero_linshen",
                    "display_name": "Lin Shen",
                    "profile_json": {"coat": "dark trench coat", "hair": "short black"},
                    "source": "agent_inferred",
                    "is_locked": False,
                },
                {
                    "asset_type": "SCENE",
                    "entity_key": "beijing_overpass",
                    "display_name": "2016 Beijing Overpass",
                    "profile_json": {"weather": "winter drizzle", "lighting": "neon streetlamp"},
                    "source": "agent_inferred",
                    "is_locked": False,
                },
            ]
        }
    )


def test_healthz_and_internal_token_auth() -> None:
    transport = httpx.MockTransport(
        lambda request: httpx.Response(200, json={"choices": [{"message": {"content": _sample_assets_json()}}]})
    )
    gw = GatewayClient("http://mock-gateway.local/proxy/v1", internal_token="test_internal_token", transport=transport)
    app = create_app(
        settings=_test_settings(),
        gateway_client=gw,
        checkpointer=MemorySaver(),
        enable_db_persistence=False,
    )

    with TestClient(app) as client:
        health = client.get("/healthz")
        assert health.status_code == 200
        assert health.json()["status"] == "ok"

        missing_auth = client.post(
            "/internal/v1/workflows/parse-story",
            json={
                "job_id": "job-1",
                "thread_id": "th-1",
                "raw_story": "Winter night on an overpass.",
            },
        )
        assert missing_auth.status_code == 401

        wrong_auth = client.post(
            "/internal/v1/workflows/parse-story",
            headers={"X-Internal-Token": "wrong_token"},
            json={
                "job_id": "job-1",
                "thread_id": "th-1",
                "raw_story": "Winter night on an overpass.",
            },
        )
        assert wrong_auth.status_code == 401


def test_gateway_client_strips_fences_repairs_json_and_maps_errors() -> None:
    calls = 0

    def handler(request: httpx.Request) -> httpx.Response:
        nonlocal calls
        calls += 1
        assert request.headers["X-Internal-Token"] == "test_internal_token"
        assert request.headers["X-Trace-ID"] == "tr_unit_01"
        assert request.headers["X-Call-Type"] == "LLM_TEXT"
        if calls == 1:
            return httpx.Response(200, json={"choices": [{"message": {"content": "```json\n{\"bad\": true}\n```"}}]})
        return httpx.Response(
            200,
            json={"choices": [{"message": {"content": f"```json\n{_sample_assets_json()}\n```"}}]},
        )

    gw = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(handler),
    )
    parsed = gw.chat_json(
        messages=[{"role": "user", "content": "Extract assets"}],
        schema_cls=ExtractedAssetsPayload,
        job_id="job-1",
        trace_id="tr_unit_01",
    )
    assert calls == 2
    assert len(parsed.assets) == 2
    assert parsed.assets[0].entity_key == "hero_linshen"

    gw_402 = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(lambda req: httpx.Response(402, json={"code": 40201})),
    )
    with pytest.raises(BudgetExceededError):
        gw_402.chat_completions([{"role": "user", "content": "hi"}], job_id="j1", trace_id="t1")

    gw_422 = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(lambda req: httpx.Response(422, json={"code": 42201})),
    )
    with pytest.raises(SafetyBlockedError):
        gw_422.chat_completions([{"role": "user", "content": "hi"}], job_id="j1", trace_id="t1")


def test_parse_story_extracts_assets_and_interrupts_before_gate1() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/proxy/v1/chat/completions"
        assert request.headers["X-Job-ID"] == "job-unit-1"
        assert request.headers["X-Trace-ID"] == "tr_parse_1"
        return httpx.Response(200, json={"choices": [{"message": {"content": _sample_assets_json()}}]})

    gw = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(handler),
    )
    app = create_app(
        settings=_test_settings(),
        gateway_client=gw,
        checkpointer=MemorySaver(),
        enable_db_persistence=False,
    )

    with TestClient(app) as client:
        resp = client.post(
            "/internal/v1/workflows/parse-story",
            headers={
                "X-Internal-Token": "test_internal_token",
                "X-Trace-ID": "tr_parse_1",
            },
            json={
                "job_id": "job-unit-1",
                "project_id": "proj-unit-1",
                "thread_id": "th_unit_parse_1",
                "raw_story": "In winter 2016, Lin Shen stood on the Beijing overpass under misty streetlights.",
                "style_preset": "cinematic_melancholy",
                "aspect_ratio": "16:9",
                "target_frames": 4,
            },
        )
        assert resp.status_code == 200
        body = resp.json()
        assert body["status"] == "WAITING_GATE"
        assert body["current_stage"] == "GATE_1"
        assert body["trace_id"] == "tr_parse_1"
        assert len(body["assets"]) == 2
        assert body["assets"][0]["entity_key"] == "hero_linshen"

        snapshot = app.state.story_graph.get_state({"configurable": {"thread_id": "th_unit_parse_1"}})
        assert tuple(snapshot.next) == ("plan_script_node",)


@pytest.mark.skipif(not os.getenv("DATABASE_URL"), reason="DATABASE_URL not set for live PG16 integration")
def test_live_pg16_postgres_saver_and_gate1_persistence() -> None:
    import psycopg

    db_url = os.environ["DATABASE_URL"]
    project_id = str(uuid.uuid4())
    job_id = str(uuid.uuid4())
    thread_id = f"th_pg_{uuid.uuid4().hex[:16]}"
    idem_key = f"idem_pg_{uuid.uuid4().hex[:16]}"

    with psycopg.connect(db_url, autocommit=True) as conn:
        conn.execute(
            """
            INSERT INTO projects (id, title, raw_story, aspect_ratio, style_preset, target_frames)
            VALUES (%s, %s, %s, '16:9', 'cinematic_melancholy', 4)
            """,
            (project_id, "PG Integration Story", "Lin Shen waits on an overpass."),
        )
        conn.execute(
            """
            INSERT INTO story_jobs (id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage)
            VALUES (%s, %s, %s, %s, 'tr_pg_01', 'RUNNING', 'EXTRACT_ASSETS')
            """,
            (job_id, project_id, idem_key, thread_id),
        )

    gw = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(
            lambda request: httpx.Response(200, json={"choices": [{"message": {"content": _sample_assets_json()}}]})
        ),
    )
    app = create_app(
        settings=_test_settings(database_url=db_url),
        gateway_client=gw,
        enable_db_persistence=True,
    )

    try:
        with TestClient(app) as client:
            resp = client.post(
                "/internal/v1/workflows/parse-story",
                headers={
                    "X-Internal-Token": "test_internal_token",
                    "X-Trace-ID": "tr_pg_01",
                },
                json={
                    "job_id": job_id,
                    "project_id": project_id,
                    "thread_id": thread_id,
                    "raw_story": "Lin Shen waits on an overpass in cold rain.",
                },
            )
            assert resp.status_code == 200
            data = resp.json()
            assert data["status"] == "WAITING_GATE"
            assert data["current_stage"] == "GATE_1"
            assert len(data["assets"]) == 2
            assert data["assets"][0]["id"] is not None

            snap = app.state.story_graph.get_state({"configurable": {"thread_id": thread_id}})
            assert tuple(snap.next) == ("plan_script_node",)

        with psycopg.connect(db_url) as conn:
            job_row = conn.execute(
                "SELECT status, current_stage FROM story_jobs WHERE id = %s",
                (job_id,),
            ).fetchone()
            assert job_row == ("WAITING_GATE", "GATE_1")

            asset_count = conn.execute(
                "SELECT COUNT(*) FROM asset_style_profiles WHERE project_id = %s",
                (project_id,),
            ).fetchone()[0]
            assert asset_count == 2

            cp_count = conn.execute(
                "SELECT COUNT(*) FROM checkpoints WHERE thread_id = %s",
                (thread_id,),
            ).fetchone()[0]
            assert cp_count >= 1
    finally:
        with psycopg.connect(db_url, autocommit=True) as conn:
            conn.execute("DELETE FROM checkpoints WHERE thread_id = %s", (thread_id,))
            conn.execute("DELETE FROM checkpoint_writes WHERE thread_id = %s", (thread_id,))
            conn.execute("DELETE FROM checkpoint_blobs WHERE thread_id = %s", (thread_id,))
            conn.execute("DELETE FROM projects WHERE id = %s", (project_id,))
