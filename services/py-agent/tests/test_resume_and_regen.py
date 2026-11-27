"""Tests for T-M2-03: Gate 2 resume, checkpoint recovery across restart, and Gate 3 regen."""

from __future__ import annotations

import json
import os
import uuid

import httpx
import pytest
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import MemorySaver

from app.adapters.gateway_client import GatewayClient
from app.config import Settings
from app.main import create_app


def _make_mock_gateway(
    assets_payload: dict | None = None,
    plan_payload: dict | None = None,
    rewrite_payload: dict | None = None,
    safety_block_regen: bool = False,
) -> GatewayClient:
    assets_data = assets_payload or {
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
                "display_name": "Beijing Overpass",
                "profile_json": {"lighting": "neon streetlamp in winter rain"},
                "source": "agent_inferred",
                "is_locked": False,
            },
        ]
    }
    plan_data = plan_payload or {
        "frames": [
            {
                "frame_index": 1,
                "shot_type": "Wide Shot",
                "camera_angle": "Eye-level",
                "composition": "Lin Shen stands at overpass edge",
                "lighting": "Dim amber neon mist",
                "action": "Lin Shen opens black umbrella on overpass",
                "dialogue": "It never really stops raining here.",
                "entities": ["hero_linshen", "beijing_overpass"],
            },
            {
                "frame_index": 2,
                "shot_type": "Medium Close-Up",
                "camera_angle": "Low angle",
                "composition": "Side profile against city bokeh",
                "lighting": "Cold blue streetlights",
                "action": "Lin Shen folds umbrella as car headlights pass",
                "dialogue": "Right on time.",
                "entities": ["hero_linshen"],
            },
            {
                "frame_index": 3,
                "shot_type": "Long Shot",
                "camera_angle": "High angle",
                "composition": "Descending overpass stairs",
                "lighting": "Reflecting wet concrete",
                "action": "Lin Shen walks down wet concrete stairs",
                "dialogue": "",
                "entities": ["hero_linshen"],
            },
        ],
        "entity_states": [
            {
                "frame_index": 1,
                "entity_key": "hero_linshen",
                "state_json": {"clothing_state": "dry shoulders", "held_items": "open umbrella"},
            },
            {
                "frame_index": 2,
                "entity_key": "hero_linshen",
                "state_json": {"clothing_state": "damp shoulders", "held_items": "folded umbrella"},
            },
            {
                "frame_index": 3,
                "entity_key": "hero_linshen",
                "state_json": {"clothing_state": "damp shoulders", "held_items": "no umbrella"},
            },
        ],
    }
    rewrite_data = rewrite_payload or {
        "rewritten_prompt": "[Style] cinematic_melancholy; Lin Shen in dark trench coat, warm sodium backlight reflection",
        "updated_entity_states": [
            {
                "frame_index": 2,
                "entity_key": "hero_linshen",
                "state_json": {
                    "clothing_state": "soaked trench shoulders",
                    "held_items": "silver flask",
                },
            }
        ],
    }

    image_counter = 0

    def handler(request: httpx.Request) -> httpx.Response:
        nonlocal image_counter
        if request.url.path.endswith("/chat/completions"):
            body = json.loads(request.content.decode("utf-8"))
            content = " ".join(m.get("content", "") for m in body.get("messages", []))
            if "Local Edit Instruction (Gate 3)" in content:
                if safety_block_regen:
                    return httpx.Response(422, json={"code": 42201, "message": "SAFETY_BLOCKED"})
                return httpx.Response(
                    200,
                    json={"choices": [{"message": {"content": json.dumps(rewrite_data)}}]},
                )
            if "sequential storyboard frames" in content:
                return httpx.Response(
                    200,
                    json={"choices": [{"message": {"content": json.dumps(plan_data)}}]},
                )
            return httpx.Response(
                200,
                json={"choices": [{"message": {"content": json.dumps(assets_data)}}]},
            )

        if request.url.path.endswith("/images/generations"):
            image_counter += 1
            return httpx.Response(
                200,
                json={
                    "data": [
                        {
                            "url": f"https://mock-cdn.echocanvas.local/img_{image_counter}.png",
                            "seed": 1000 + image_counter,
                        }
                    ]
                },
            )
        return httpx.Response(404)

    return GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(handler),
    )


def test_gate1_to_gate2_resume_and_gate3_regen_in_memory() -> None:
    app = create_app(
        settings=Settings(internal_auth_token="test_internal_token"),
        gateway_client=_make_mock_gateway(),
        checkpointer=MemorySaver(),
        enable_db_persistence=False,
    )
    headers = {"X-Internal-Token": "test_internal_token", "X-Trace-ID": "tr_m203_unit"}

    with TestClient(app) as client:
        r_parse = client.post(
            "/internal/v1/workflows/parse-story",
            headers=headers,
            json={
                "job_id": "job-1",
                "project_id": "proj-1",
                "thread_id": "th-unit-1",
                "raw_story": "Lin Shen crosses the rainy overpass.",
                "target_frames": 3,
            },
        )
        assert r_parse.status_code == 200
        assert r_parse.json()["current_stage"] == "GATE_1"

        # Resume Gate 1 with user ground truth edit -> pauses at Gate 2
        r_g1 = client.post(
            "/internal/v1/workflows/resume",
            headers=headers,
            json={
                "thread_id": "th-unit-1",
                "job_id": "job-1",
                "project_id": "proj-1",
                "gate_no": 1,
                "user_edits": {
                    "assets": [
                        {
                            "entity_key": "hero_linshen",
                            "profile_json": {"coat": "dark wool trench coat", "scarf": "grey knit"},
                            "is_locked": True,
                        }
                    ]
                },
            },
        )
        assert r_g1.status_code == 200
        g1_body = r_g1.json()
        assert g1_body["status"] == "WAITING_GATE"
        assert g1_body["current_stage"] == "GATE_2"
        assert len(g1_body["frames"]) == 3
        frame1_id = g1_body["frames"][0]["id"]
        frame2_id = g1_body["frames"][1]["id"]

        # Verify asset became locked user_ground_truth
        locked = app.state.memory_repo.list_locked_assets("proj-1")
        assert len(locked) == 1
        assert locked[0].source == "user_ground_truth"
        assert locked[0].profile_json["scarf"] == "grey knit"

        # Confirm Gate 2 with Frame 2 dialogue edit
        r_g2 = client.post(
            "/internal/v1/workflows/resume",
            headers=headers,
            json={
                "thread_id": "th-unit-1",
                "job_id": "job-1",
                "project_id": "proj-1",
                "gate_no": 2,
                "user_edits": {
                    "frames": [{"frame_index": 2, "dialogue": "Updated Gate 2 line."}]
                },
            },
        )
        assert r_g2.status_code == 200
        g2_body = r_g2.json()
        assert g2_body["status"] == "RUNNING"
        assert g2_body["current_stage"] == "RENDER_FRAMES"
        assert g2_body["frames"][1]["dialogue"] == "Updated Gate 2 line."

        # Render Frame 1 & Frame 2 initial versions (v1)
        v1_f1 = client.post(f"/internal/v1/frames/{frame1_id}/render", headers=headers, json={})
        assert v1_f1.status_code == 200
        assert v1_f1.json()["version_no"] == 1

        v1_f2 = client.post(f"/internal/v1/frames/{frame2_id}/render", headers=headers, json={})
        assert v1_f2.status_code == 200
        assert v1_f2.json()["version_no"] == 1

        # Gate 3 regenerate Frame 2 with cascade_downstream=True
        v2_f2 = client.post(
            f"/internal/v1/frames/{frame2_id}/regenerate",
            headers=headers,
            json={
                "user_edit_note": "Make lighting warmer and swap umbrella for silver flask",
                "cascade_downstream": True,
            },
        )
        assert v2_f2.status_code == 200
        regen_body = v2_f2.json()
        assert regen_body["version_no"] == 2
        assert regen_body["cascaded_frame_indices"] == [3]
        assert app.state.story_repo.get_frame(frame2_id)["active_version_id"] == regen_body["id"]

        f3_states = app.state.memory_repo.get_episodic_states("job-1", 3)
        assert f3_states[0].state_json["held_items"] == "silver flask"


def test_safety_blocked_regen_preserves_existing_version() -> None:
    app = create_app(
        settings=Settings(internal_auth_token="test_internal_token"),
        gateway_client=_make_mock_gateway(safety_block_regen=True),
        checkpointer=MemorySaver(),
        enable_db_persistence=False,
    )
    headers = {"X-Internal-Token": "test_internal_token"}
    with TestClient(app) as client:
        client.post(
            "/internal/v1/workflows/parse-story",
            headers=headers,
            json={"job_id": "j-safe", "project_id": "p-safe", "thread_id": "th-safe", "raw_story": "story", "target_frames": 3},
        )
        g1 = client.post(
            "/internal/v1/workflows/resume",
            headers=headers,
            json={"thread_id": "th-safe", "gate_no": 1},
        )
        f1_id = g1.json()["frames"][0]["id"]
        v1 = client.post(f"/internal/v1/frames/{f1_id}/render", headers=headers, json={})
        assert v1.status_code == 200
        v1_id = v1.json()["id"]

        blocked = client.post(
            f"/internal/v1/frames/{f1_id}/regenerate",
            headers=headers,
            json={"user_edit_note": "blocked request"},
        )
        assert blocked.status_code == 422
        assert app.state.story_repo.get_frame(f1_id)["active_version_id"] == v1_id


@pytest.mark.skipif(not os.getenv("DATABASE_URL"), reason="DATABASE_URL not set for live PG16 integration")
def test_live_pg16_checkpoint_restart_recovery_and_gate3_regen() -> None:
    import psycopg

    db_url = os.environ["DATABASE_URL"]
    project_id = str(uuid.uuid4())
    job_id = str(uuid.uuid4())
    thread_id = f"th_rec_{uuid.uuid4().hex[:16]}"
    idem_key = f"idem_rec_{uuid.uuid4().hex[:16]}"

    with psycopg.connect(db_url, autocommit=True) as conn:
        conn.execute(
            "INSERT INTO projects (id, title, raw_story, target_frames) VALUES (%s, 'Restart Test', 'Rainy night', 3)",
            (project_id,),
        )
        conn.execute(
            """
            INSERT INTO story_jobs (id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage)
            VALUES (%s, %s, %s, %s, 'tr_rec_01', 'RUNNING', 'EXTRACT_ASSETS')
            """,
            (job_id, project_id, idem_key, thread_id),
        )

    headers = {"X-Internal-Token": "test_internal_token", "X-Trace-ID": "tr_rec_01"}
    try:
        # Phase 1: First Sidecar instance runs parse-story (Gate 1) -> resume Gate 1 -> pauses at Gate 2
        app1 = create_app(
            settings=Settings(database_url=db_url, internal_auth_token="test_internal_token"),
            gateway_client=_make_mock_gateway(),
            enable_db_persistence=True,
        )
        with TestClient(app1) as c1:
            r1 = c1.post(
                "/internal/v1/workflows/parse-story",
                headers=headers,
                json={
                    "job_id": job_id,
                    "project_id": project_id,
                    "thread_id": thread_id,
                    "raw_story": "Rainy night on Beijing overpass.",
                    "target_frames": 3,
                },
            )
            assert r1.status_code == 200

            g1 = c1.post(
                "/internal/v1/workflows/resume",
                headers=headers,
                json={
                    "thread_id": thread_id,
                    "gate_no": 1,
                    "user_edits": {
                        "assets": [
                            {
                                "entity_key": "hero_linshen",
                                "profile_json": {"coat": "dark trench coat", "eye_color": "dark brown"},
                                "is_locked": True,
                            }
                        ]
                    },
                },
            )
            assert g1.status_code == 200
            assert g1.json()["current_stage"] == "GATE_2"
            assert tuple(app1.state.story_graph.get_state({"configurable": {"thread_id": thread_id}}).next) == (
                "dispatch_render_node",
            )

        # Phase 2: Simulate Sidecar process restart with a brand-new FastAPI app & PostgresSaver
        app2 = create_app(
            settings=Settings(database_url=db_url, internal_auth_token="test_internal_token"),
            gateway_client=_make_mock_gateway(),
            enable_db_persistence=True,
        )
        with TestClient(app2) as c2:
            recovered = app2.state.story_graph.get_state({"configurable": {"thread_id": thread_id}})
            assert tuple(recovered.next) == ("dispatch_render_node",)

            g2 = c2.post(
                "/internal/v1/workflows/resume",
                headers=headers,
                json={
                    "thread_id": thread_id,
                    "gate_no": 2,
                    "user_edits": {
                        "frames": [{"frame_index": 2, "dialogue": "Recovered across Sidecar restart."}]
                    },
                },
            )
            assert g2.status_code == 200
            frames = g2.json()["frames"]
            assert len(frames) == 3
            assert frames[1]["dialogue"] == "Recovered across Sidecar restart."
            frame1_id, frame2_id = frames[0]["id"], frames[1]["id"]

            r_f1 = c2.post(f"/internal/v1/frames/{frame1_id}/render", headers=headers, json={})
            assert r_f1.status_code == 200
            r_f2 = c2.post(f"/internal/v1/frames/{frame2_id}/render", headers=headers, json={})
            assert r_f2.status_code == 200

            regen = c2.post(
                f"/internal/v1/frames/{frame2_id}/regenerate",
                headers=headers,
                json={"user_edit_note": "Add silver flask prop", "cascade_downstream": True},
            )
            assert regen.status_code == 200
            assert regen.json()["version_no"] == 2
            assert regen.json()["cascaded_frame_indices"] == [3]

        with psycopg.connect(db_url) as conn:
            ver_count = conn.execute(
                "SELECT COUNT(*) FROM frame_versions WHERE frame_id = %s",
                (frame2_id,),
            ).fetchone()[0]
            assert ver_count == 2
            active_ver = conn.execute(
                "SELECT fv.version_no FROM story_frames sf JOIN frame_versions fv ON fv.id = sf.active_version_id WHERE sf.id = %s",
                (frame2_id,),
            ).fetchone()[0]
            assert active_ver == 2
            f3_diff = conn.execute(
                "SELECT state_json, diff_from_prev FROM episodic_entity_states WHERE job_id = %s AND frame_index = 3 AND entity_key = 'hero_linshen'",
                (job_id,),
            ).fetchone()
            assert f3_diff is not None
            assert f3_diff[0]["inherited_regen_note"] == "Add silver flask prop"
    finally:
        with psycopg.connect(db_url, autocommit=True) as conn:
            conn.execute("DELETE FROM checkpoints WHERE thread_id = %s", (thread_id,))
            conn.execute("DELETE FROM checkpoint_writes WHERE thread_id = %s", (thread_id,))
            conn.execute("DELETE FROM checkpoint_blobs WHERE thread_id = %s", (thread_id,))
            conn.execute("DELETE FROM projects WHERE id = %s", (project_id,))
