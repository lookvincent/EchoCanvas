"""Unit and live PG16 tests for T-M2-02 layered memory engine and WorkingContextBuilder."""

from __future__ import annotations

import os
import uuid

import httpx
import pytest
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import MemorySaver

from app.adapters.gateway_client import GatewayClient
from app.config import Settings
from app.main import create_app
from app.memory.embeddings import EMBEDDING_DIM, compute_deterministic_embedding
from app.memory.repository import InMemoryMemoryRepository, MemoryRepository, compute_state_diff
from app.memory.working_context import WorkingContextBuilder, merge_profile_dicts
from app.schemas import AssetCard, EntityStateSnapshot, FrameScript


def test_user_ground_truth_overrides_agent_inferred_and_logs_conflicts() -> None:
    merged, conflicts = merge_profile_dicts(
        base_profile={"coat": "charcoal trench coat", "scarf": "grey wool"},
        base_source="user_ground_truth",
        incoming_profile={"coat": "bright yellow jacket", "hair": "short black"},
        incoming_source="agent_inferred",
        entity_label="hero_linshen",
    )
    assert merged["coat"] == "charcoal trench coat"
    assert merged["scarf"] == "grey wool"
    assert merged["hair"] == "short black"
    assert conflicts == ["hero_linshen.coat"]

    repo = InMemoryMemoryRepository()
    repo.upsert_assets(
        "proj-1",
        [
            AssetCard(
                asset_type="CHARACTER",
                entity_key="hero_linshen",
                display_name="Lin Shen",
                profile_json={"coat": "charcoal trench coat"},
                reference_image_url="https://cdn.local/hero_ref.png",
                source="user_ground_truth",
                is_locked=True,
            ),
            AssetCard(
                asset_type="SCENE",
                entity_key="overpass_neon",
                display_name="Beijing Overpass",
                profile_json={"lights": "sodium streetlamps and rain puddles"},
                source="agent_inferred",
                is_locked=False,
            ),
            AssetCard(
                asset_type="SCENE",
                entity_key="subway_car",
                display_name="Subway Interior",
                profile_json={"seats": "stainless steel benches"},
                source="agent_inferred",
                is_locked=False,
            ),
        ],
    )

    # Attempted agent_inferred upsert on same entity_key must NOT overwrite user_ground_truth.
    saved = repo.upsert_assets(
        "proj-1",
        [
            AssetCard(
                asset_type="CHARACTER",
                entity_key="hero_linshen",
                display_name="Corrupted Hero",
                profile_json={"coat": "neon pink hoodie"},
                source="agent_inferred",
                is_locked=False,
            )
        ],
    )
    assert saved[0].source == "user_ground_truth"
    assert saved[0].profile_json["coat"] == "charcoal trench coat"

    # Episodic states at frame 1 -> frame 2 diff and working context conflict check.
    repo.upsert_episodic_states(
        "job-1",
        frame_index=1,
        states=[
            EntityStateSnapshot(
                frame_index=1,
                entity_key="hero_linshen",
                state_json={
                    "coat": "neon pink hoodie",  # conflicts with user_ground_truth!
                    "clothing_state": "wet shoulders",
                    "held_items": "folded black umbrella",
                },
            )
        ],
    )

    builder = WorkingContextBuilder(repo)
    ctx = builder.build(
        project_id="proj-1",
        job_id="job-1",
        frame_index=2,
        frame_script=FrameScript(
            frame_index=2,
            shot_type="Medium Shot",
            camera_angle="Eye-level",
            composition="Rule of thirds on overpass bridge",
            lighting="Neon streetlamp reflections on wet concrete",
            action="Lin Shen pauses mid-bridge holding a folded umbrella",
            dialogue="Ignore this dialogue from script when building visual diff",
            entities=["hero_linshen", "overpass_neon"],
        ),
        style_preset="cinematic_melancholy",
        max_prompt_chars=1200,
        top_k=1,
    )

    assert "hero_linshen.coat" in ctx.dropped_conflicts
    assert "charcoal trench coat" in ctx.assembled_prompt
    assert "neon pink hoodie" not in ctx.assembled_prompt
    assert "wet shoulders" in ctx.assembled_prompt
    assert "Beijing Overpass" in ctx.assembled_prompt
    assert ctx.reference_image_url == "https://cdn.local/hero_ref.png"


def test_episodic_diff_budget_trimming_and_frame1_vs_frame2_anchor() -> None:
    diff = compute_state_diff(
        {"clothing": "dry trench coat", "umbrella": "closed"},
        {"clothing": "soaked trench coat", "umbrella": "open", "location": "overpass"},
    )
    assert diff == {
        "clothing": {"from": "dry trench coat", "to": "soaked trench coat"},
        "location": {"from": None, "to": "overpass"},
        "umbrella": {"from": "closed", "to": "open"},
    }

    vec = compute_deterministic_embedding("Beijing winter night rain overpass")
    assert len(vec) == EMBEDDING_DIM

    repo = InMemoryMemoryRepository()
    repo.upsert_assets(
        "proj-trim",
        [
            AssetCard(
                asset_type="CHARACTER",
                entity_key="hero_a",
                display_name="Hero A",
                profile_json={"coat": "navy coat"},
                source="user_ground_truth",
                is_locked=True,
            ),
            AssetCard(
                asset_type="SCENE",
                entity_key="bg_long",
                display_name="Verbose Background",
                profile_json={"details": "detail " * 80},
                source="agent_inferred",
                is_locked=False,
            ),
        ],
    )
    repo.set_first_frame_reference_image("job-trim", "https://cdn.local/frame_1_active.png")

    builder = WorkingContextBuilder(repo)
    f1 = builder.build(
        project_id="proj-trim",
        job_id="job-trim",
        frame_index=1,
        query_text="Hero A enters empty alley",
        max_prompt_chars=180,
        top_k=3,
    )
    assert f1.reference_image_url is None

    f2 = builder.build(
        project_id="proj-trim",
        job_id="job-trim",
        frame_index=2,
        query_text="Hero A walks through verbose background alley",
        max_prompt_chars=180,
        top_k=3,
    )
    assert f2.trimmed is True
    assert len(f2.assembled_prompt) <= 180
    assert "Hero A" in f2.assembled_prompt
    assert f2.retrieved_assets == []  # background dropped first under tight budget
    assert f2.reference_image_url == "https://cdn.local/frame_1_active.png"


@pytest.mark.skipif(not os.getenv("DATABASE_URL"), reason="DATABASE_URL not set for live PG16 integration")
def test_live_pg16_pgvector_retrieval_and_memory_endpoint() -> None:
    import psycopg
    from psycopg.rows import dict_row
    from psycopg_pool import ConnectionPool

    db_url = os.environ["DATABASE_URL"]
    project_id = str(uuid.uuid4())
    job_id = str(uuid.uuid4())
    thread_id = f"th_mem_{uuid.uuid4().hex[:16]}"
    idem_key = f"idem_mem_{uuid.uuid4().hex[:16]}"
    frame1_id = str(uuid.uuid4())
    version1_id = str(uuid.uuid4())

    with psycopg.connect(db_url, autocommit=True) as conn:
        conn.execute(
            """
            INSERT INTO projects (id, title, raw_story)
            VALUES (%s, 'PG Memory Test', 'Winter overpass scene')
            """,
            (project_id,),
        )
        conn.execute(
            """
            INSERT INTO story_jobs (id, project_id, idempotency_key, langgraph_thread_id, trace_id)
            VALUES (%s, %s, %s, %s, 'tr_mem_01')
            """,
            (job_id, project_id, idem_key, thread_id),
        )
        conn.execute(
            """
            INSERT INTO story_frames (id, job_id, frame_index, script_json)
            VALUES (%s, %s, 1, '{"action":"stand"}'::jsonb)
            """,
            (frame1_id, job_id),
        )
        conn.execute(
            """
            INSERT INTO frame_versions (id, frame_id, version_no, prompt_snapshot, image_url)
            VALUES (%s, %s, 1, 'initial prompt', 'https://mock-s3.local/frame1_anchor.png')
            """,
            (version1_id, frame1_id),
        )
        conn.execute(
            "UPDATE story_frames SET active_version_id = %s WHERE id = %s",
            (version1_id, frame1_id),
        )

    pool = ConnectionPool(
        conninfo=db_url,
        min_size=1,
        max_size=4,
        open=True,
        kwargs={"autocommit": True, "prepare_threshold": 0, "row_factory": dict_row},
    )

    try:
        repo = MemoryRepository(pool)
        repo.upsert_assets(
            project_id,
            [
                AssetCard(
                    asset_type="CHARACTER",
                    entity_key="hero_linshen",
                    display_name="Lin Shen",
                    profile_json={"coat": "dark trench coat", "scarf": "grey wool"},
                    source="user_ground_truth",
                    is_locked=True,
                ),
                AssetCard(
                    asset_type="SCENE",
                    entity_key="overpass_bridge",
                    display_name="Beijing Winter Overpass",
                    profile_json={"setting": "neon streetlamp wet overpass in winter rain"},
                    source="agent_inferred",
                    is_locked=False,
                ),
                AssetCard(
                    asset_type="SCENE",
                    entity_key="desert_canyon",
                    display_name="Red Rock Canyon",
                    profile_json={"setting": "arid red sandstone desert under midday sun"},
                    source="agent_inferred",
                    is_locked=False,
                ),
            ],
        )

        # Agent-inferred update must NOT overwrite locked user_ground_truth in PostgreSQL.
        after = repo.upsert_assets(
            project_id,
            [
                AssetCard(
                    asset_type="CHARACTER",
                    entity_key="hero_linshen",
                    display_name="Overwritten Name",
                    profile_json={"coat": "white suit"},
                    source="agent_inferred",
                    is_locked=False,
                )
            ],
        )
        assert after[0].display_name == "Lin Shen"
        assert after[0].source == "user_ground_truth"
        assert after[0].profile_json["coat"] == "dark trench coat"

        # pgvector Top-1 retrieval should rank overpass_bridge ahead of desert_canyon.
        top_matches = repo.search_similar_assets(
            project_id,
            query_text="neon streetlamp wet overpass in winter rain",
            top_k=1,
            exclude_entity_keys={"hero_linshen"},
        )
        assert len(top_matches) == 1
        assert top_matches[0].entity_key == "overpass_bridge"

        repo.upsert_episodic_states(
            job_id,
            frame_index=1,
            states=[
                EntityStateSnapshot(
                    frame_index=1,
                    entity_key="hero_linshen",
                    state_json={"clothing_state": "damp shoulders", "umbrella": "folded"},
                )
            ],
        )

        # Verify FastAPI POST /internal/v1/memory/retrieve endpoint against live DB if mounted.
        gw = GatewayClient(
            "http://mock-gateway.local/proxy/v1",
            internal_token="test_internal_token",
            transport=httpx.MockTransport(lambda req: httpx.Response(200, json={})),
        )
        app = create_app(
            settings=Settings(database_url=db_url, internal_auth_token="test_internal_token"),
            gateway_client=gw,
            checkpointer=MemorySaver(),
            enable_db_persistence=True,
        )
        with TestClient(app) as client:
            if any(getattr(r, "path", None) == "/internal/v1/memory/retrieve" for r in app.routes):
                resp = client.post(
                    "/internal/v1/memory/retrieve",
                    headers={"X-Internal-Token": "test_internal_token"},
                    json={
                        "project_id": project_id,
                        "job_id": job_id,
                        "frame_index": 2,
                        "query_text": "neon streetlamp wet overpass in winter rain",
                        "top_k": 1,
                    },
                )
                assert resp.status_code == 200
                data = resp.json()
                assert "dark trench coat" in data["assembled_prompt"]
                assert data["reference_image_url"] == "https://mock-s3.local/frame1_anchor.png"
    finally:
        pool.close()
        with psycopg.connect(db_url, autocommit=True) as conn:
            conn.execute("DELETE FROM projects WHERE id = %s", (project_id,))
