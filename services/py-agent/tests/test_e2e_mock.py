"""End-to-end Go Control Plane <-> Python Sidecar mock integration test (T-M2-04).

Exercises the complete lifecycle via live Go REST/SSE APIs backed by:
  - Live Docker PostgreSQL 16 (+ pgvector & LangGraph PostgresSaver checkpoints)
  - Live Docker Redis 7 (Asynq queues + Redis Streams SSE Last-Event-ID replay)
  - Compiled Go Control Plane & AIGC Gateway binary
  - Python FastAPI Sidecar process (restarted mid-flight between Gate 1 & Gate 2!)
  - In-process Mock Upstream LLM & Vision HTTP server
"""

from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import httpx
import pytest


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


class _MockUpstreamState:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.chat_calls: list[dict[str, Any]] = []
        self.image_calls: list[dict[str, Any]] = []


def _start_mock_upstream(port: int) -> tuple[ThreadingHTTPServer, _MockUpstreamState]:
    state = _MockUpstreamState()

    assets_payload = {
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
                "profile_json": {"lighting": "amber sodium streetlamps", "weather": "winter rain"},
                "source": "agent_inferred",
                "is_locked": False,
            },
        ]
    }
    plan_payload = {
        "frames": [
            {
                "frame_index": 1,
                "shot_type": "Wide Shot",
                "camera_angle": "Eye-level",
                "composition": "Lin Shen stands at the center of the wet overpass",
                "lighting": "Cold amber neon glow",
                "action": "Lin Shen opens a black umbrella in the winter drizzle",
                "dialogue": "Still waiting.",
                "entities": ["hero_linshen", "beijing_overpass"],
            },
            {
                "frame_index": 2,
                "shot_type": "Medium Shot",
                "camera_angle": "Low angle",
                "composition": "Close profile with blurred car headlights behind",
                "lighting": "Reflected cyan streetlamp light",
                "action": "Lin Shen lowers the umbrella and checks an old watch",
                "dialogue": "Not long now.",
                "entities": ["hero_linshen"],
            },
            {
                "frame_index": 3,
                "shot_type": "Long Shot",
                "camera_angle": "High angle",
                "composition": "Empty stairwell receding into mist",
                "lighting": "Moody damp concrete reflections",
                "action": "Lin Shen steps down the wet overpass staircase",
                "dialogue": "",
                "entities": ["hero_linshen", "beijing_overpass"],
            },
        ],
        "entity_states": [
            {
                "frame_index": 1,
                "entity_key": "hero_linshen",
                "state_json": {"clothing_state": "dry shoulders", "held_items": "open black umbrella"},
            },
            {
                "frame_index": 2,
                "entity_key": "hero_linshen",
                "state_json": {"clothing_state": "damp shoulders", "held_items": "folded umbrella"},
            },
            {
                "frame_index": 3,
                "entity_key": "hero_linshen",
                "state_json": {"clothing_state": "damp shoulders", "held_items": "folded umbrella"},
            },
        ],
    }
    rewrite_payload = {
        "rewritten_prompt": "[Style] cinematic_melancholy; Lin Shen in dark charcoal trench coat holding silver flask under warm sodium backlight",
        "updated_entity_states": [
            {
                "frame_index": 2,
                "entity_key": "hero_linshen",
                "state_json": {"clothing_state": "soaked trench coat", "held_items": "silver flask"},
            }
        ],
    }

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args: Any) -> None:
            return

        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            raw = self.rfile.read(length) if length > 0 else b"{}"
            body = json.loads(raw.decode("utf-8"))

            if self.path.endswith("/chat/completions"):
                messages = " ".join(m.get("content", "") for m in body.get("messages", []))
                with state.lock:
                    state.chat_calls.append(body)
                if "Local Edit Instruction (Gate 3)" in messages:
                    content = json.dumps(rewrite_payload)
                elif "sequential storyboard frames" in messages:
                    content = json.dumps(plan_payload)
                else:
                    content = json.dumps(assets_payload)
                resp = {
                    "id": "chatcmpl_mock",
                    "choices": [{"message": {"role": "assistant", "content": content}}],
                    "usage": {"prompt_tokens": 350, "completion_tokens": 140},
                }
            elif self.path.endswith("/images/generations"):
                with state.lock:
                    idx = len(state.image_calls) + 1
                    state.image_calls.append(body)
                resp = {
                    "data": [
                        {
                            "url": f"https://mock-cdn.echocanvas.local/e2e/frame_{idx}.png",
                            "seed": 5000 + idx,
                        }
                    ]
                }
            else:
                self.send_response(404)
                self.end_headers()
                return

            out = json.dumps(resp).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(out)))
            self.end_headers()
            self.wfile.write(out)

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, state


def _wait_health(
    url: str,
    proc: subprocess.Popen[str] | None = None,
    log_path: Path | None = None,
    timeout_sec: float = 20.0,
) -> None:
    deadline = time.time() + timeout_sec
    last_err = ""
    with httpx.Client(trust_env=False, timeout=2.0) as client:
        while time.time() < deadline:
            if proc is not None and proc.poll() is not None:
                log_tail = (
                    log_path.read_text(encoding="utf-8", errors="replace")[-2000:]
                    if log_path and log_path.is_file()
                    else ""
                )
                raise AssertionError(
                    f"Process exited prematurely with code {proc.returncode} waiting for {url}.\nLog:\n{log_tail}"
                )
            try:
                r = client.get(url)
                if r.status_code == 200:
                    return
                last_err = f"status={r.status_code}"
            except Exception as exc:
                last_err = str(exc)
            time.sleep(0.15)
    log_tail = (
        log_path.read_text(encoding="utf-8", errors="replace")[-2000:]
        if log_path and log_path.is_file()
        else ""
    )
    raise AssertionError(
        f"Service at {url} did not become healthy within {timeout_sec}s ({last_err}).\nLog:\n{log_tail}"
    )


def _stop_proc(proc: subprocess.Popen[str] | None) -> None:
    if proc is None or proc.poll() is not None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=8)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)


@pytest.mark.skipif(
    not os.getenv("DATABASE_URL") or not os.getenv("REDIS_ADDR"),
    reason="DATABASE_URL and REDIS_ADDR required for live Go<->Python E2E integration test",
)
def test_go_python_full_lifecycle_e2e_with_sidecar_restart_recovery(tmp_path: Path) -> None:
    import psycopg

    db_url = os.environ["DATABASE_URL"]
    redis_addr = os.environ["REDIS_ADDR"]
    py_dir = Path(__file__).resolve().parents[1]
    go_dir = py_dir.parent / "go-gateway"

    go_exe_name = "go-gateway-e2e.exe" if os.name == "nt" else "go-gateway-e2e"
    go_bin = tmp_path / go_exe_name
    subprocess.run(
        ["go", "build", "-o", str(go_bin), "./cmd/server"],
        cwd=str(go_dir),
        check=True,
        capture_output=True,
        text=True,
    )

    mock_port = _free_port()
    py_port = _free_port()
    go_port = _free_port()
    internal_token = "test_e2e_internal_token"

    mock_server, mock_state = _start_mock_upstream(mock_port)

    common_env = os.environ.copy()
    common_env.update(
        {
            "NO_PROXY": "127.0.0.1,localhost",
            "no_proxy": "127.0.0.1,localhost",
            "DATABASE_URL": db_url,
            "REDIS_ADDR": redis_addr,
            "INTERNAL_AUTH_TOKEN": internal_token,
            "GO_GATEWAY_PORT": str(go_port),
            "PY_SIDECAR_URL": f"http://127.0.0.1:{py_port}",
            "GO_GATEWAY_INTERNAL_PROXY_URL": f"http://127.0.0.1:{go_port}/proxy/v1",
            "LLM_PRIMARY_PROVIDER": "deepseek",
            "LLM_PRIMARY_BASE_URL": f"http://127.0.0.1:{mock_port}/v1",
            "LLM_PRIMARY_MODEL": "deepseek-chat",
            "LLM_PRIMARY_API_KEY": "test_mock_llm_key",
            "VISION_PRIMARY_PROVIDER": "siliconflow",
            "VISION_PRIMARY_BASE_URL": f"http://127.0.0.1:{mock_port}/v1",
            "VISION_PRIMARY_MODEL": "black-forest-labs/FLUX.1-dev",
            "VISION_PRIMARY_API_KEY": "test_mock_vision_key",
            "GIN_MODE": "test",
            "LOG_LEVEL": "warn",
        }
    )

    py_proc: subprocess.Popen[str] | None = None
    go_proc: subprocess.Popen[str] | None = None
    py_log = tmp_path / "py_sidecar.log"
    go_log = tmp_path / "go_gateway.log"
    created_project_id: str | None = None
    created_thread_id: str | None = None

    def launch_py() -> subprocess.Popen[str]:
        with open(py_log, "a", encoding="utf-8") as py_f:
            proc = subprocess.Popen(
                [
                    sys.executable,
                    "-m",
                    "uvicorn",
                    "app.main:app",
                    "--host",
                    "127.0.0.1",
                    "--port",
                    str(py_port),
                    "--log-level",
                    "warning",
                ],
                cwd=str(py_dir),
                env=common_env,
                stdout=py_f,
                stderr=subprocess.STDOUT,
                text=True,
            )
        _wait_health(f"http://127.0.0.1:{py_port}/healthz", proc=proc, log_path=py_log)
        return proc

    try:
        py_proc = launch_py()
        with open(go_log, "a", encoding="utf-8") as go_f:
            go_proc = subprocess.Popen(
                [str(go_bin)],
                cwd=str(go_dir),
                env=common_env,
                stdout=go_f,
                stderr=subprocess.STDOUT,
                text=True,
            )
        _wait_health(f"http://127.0.0.1:{go_port}/healthz", proc=go_proc, log_path=go_log)

        base = f"http://127.0.0.1:{go_port}"
        with httpx.Client(base_url=base, timeout=15.0, trust_env=False) as client:
            # 1. Create Project
            p_resp = client.post(
                "/api/v1/projects",
                json={
                    "title": "E2E Night Overpass",
                    "raw_story": "Winter drizzle on a 2016 Beijing overpass.",
                    "aspect_ratio": "16:9",
                    "style_preset": "cinematic_melancholy",
                    "target_frames": 3,
                },
            )
            assert p_resp.status_code == 201, p_resp.text
            created_project_id = p_resp.json()["data"]["id"]

            # 2. Idempotent Job Creation -> 201 then 200
            idem_key = f"e2e_idem_{uuid.uuid4().hex[:12]}"
            j1 = client.post(
                f"/api/v1/projects/{created_project_id}/jobs",
                headers={"X-Idempotency-Key": idem_key, "X-Trace-ID": "tr_e2e_start"},
                json={"budget_limit_usd": 2.0},
            )
            assert j1.status_code == 201, j1.text
            job_id = j1.json()["data"]["id"]
            created_thread_id = j1.json()["data"]["langgraph_thread_id"]

            j_dup = client.post(
                f"/api/v1/projects/{created_project_id}/jobs",
                headers={"X-Idempotency-Key": idem_key},
                json={"budget_limit_usd": 2.0},
            )
            assert j_dup.status_code == 200
            assert j_dup.json()["data"]["id"] == job_id

            # 3. Wait for Async Gate 1 pause (WAITING_GATE / GATE_1)
            deadline = time.time() + 15.0
            detail: dict[str, Any] = {}
            while time.time() < deadline:
                g = client.get(f"/api/v1/jobs/{job_id}")
                assert g.status_code == 200
                detail = g.json()["data"]
                if detail["job"]["status"] == "WAITING_GATE" and detail["job"]["current_stage"] == "GATE_1":
                    break
                assert detail["job"]["status"] != "FAILED", f"Job failed unexpectedly: {detail}"
                time.sleep(0.15)
            assert detail["job"]["current_stage"] == "GATE_1", f"Timed out waiting for GATE_1: {detail}"

            # 4. Verify SSE Redis Streams Last-Event-ID replay contains gate_waiting
            sse_chunks: list[str] = []
            try:
                with client.stream(
                    "GET",
                    f"/api/v1/jobs/{job_id}/events",
                    headers={"Last-Event-ID": "0-0"},
                    timeout=0.8,
                ) as sse_resp:
                    assert sse_resp.status_code == 200
                    for line in sse_resp.iter_lines():
                        sse_chunks.append(line)
                        if "event: gate_waiting" in line:
                            break
            except httpx.ReadTimeout:
                pass
            sse_text = "\n".join(sse_chunks)
            assert "gate_waiting" in sse_text, f"Expected gate_waiting in SSE stream: {sse_text}"

            # 5. User edits & locks main character -> source flips to user_ground_truth
            assets_resp = client.get(f"/api/v1/projects/{created_project_id}/assets")
            assert assets_resp.status_code == 200
            assets = assets_resp.json()["data"]["items"]
            assert len(assets) >= 2
            hero = next(a for a in assets if a["entity_key"] == "hero_linshen")
            upd_asset = client.put(
                f"/api/v1/assets/{hero['id']}",
                json={
                    "profile_json": {"coat": "dark charcoal trench coat", "hair": "short black"},
                    "is_locked": True,
                },
            )
            assert upd_asset.status_code == 200
            assert upd_asset.json()["data"]["source"] == "user_ground_truth"
            assert upd_asset.json()["data"]["is_locked"] is True

            # 6. Confirm Gate 1 -> synchronous storyboard breakdown paused at GATE_2
            c1 = client.post(f"/api/v1/jobs/{job_id}/gates/1/confirm", json={})
            assert c1.status_code == 200, c1.text
            g1_detail = c1.json()["data"]
            assert g1_detail["job"]["status"] == "WAITING_GATE"
            assert g1_detail["job"]["current_stage"] == "GATE_2"
            assert len(g1_detail["frames"]) == 3

            # 7. Kill Python Sidecar completely & restart fresh process (proves PG PostgresSaver recovery)
            _stop_proc(py_proc)
            py_proc = launch_py()

            # 8. Confirm Gate 2 with Frame 2 dialogue edit -> triggers Go Asynq Frame 1..3 renders
            c2 = client.post(
                f"/api/v1/jobs/{job_id}/gates/2/confirm",
                json={"user_edits": {"frames": [{"frame_index": 2, "dialogue": "Survived Sidecar restart!"}]}},
            )
            assert c2.status_code == 200, c2.text
            assert c2.json()["data"]["frames"][1]["script_json"]["dialogue"] == "Survived Sidecar restart!"

            # 9. Poll until all 3 frames render and job hits SUCCEEDED / DONE
            render_deadline = time.time() + 20.0
            done_detail: dict[str, Any] = {}
            while time.time() < render_deadline:
                g = client.get(f"/api/v1/jobs/{job_id}")
                done_detail = g.json()["data"]
                if done_detail["job"]["status"] == "SUCCEEDED" and done_detail["job"]["current_stage"] == "DONE":
                    break
                assert done_detail["job"]["status"] != "FAILED", f"Job failed during render: {done_detail}"
                time.sleep(0.2)
            assert done_detail["job"]["status"] == "SUCCEEDED", f"Timed out rendering: {done_detail}"
            assert done_detail["job"]["current_stage"] == "DONE"
            assert len(done_detail["versions"]) == 3
            assert all(t["status"] == "SUCCEEDED" for t in done_detail["tasks"])

            # 10. Check SSE stream contains frame_completed and job_completed
            final_sse_lines: list[str] = []
            try:
                with client.stream(
                    "GET",
                    f"/api/v1/jobs/{job_id}/events",
                    headers={"Last-Event-ID": "0-0"},
                    timeout=1.0,
                ) as sse_resp:
                    for line in sse_resp.iter_lines():
                        final_sse_lines.append(line)
                        if "event: job_completed" in line:
                            break
            except httpx.ReadTimeout:
                pass
            final_sse_text = "\n".join(final_sse_lines)
            assert "frame_completed" in final_sse_text
            assert "job_completed" in final_sse_text

            # 11. Check Usage Ledger recorded LLM_TEXT + IMAGE_GEN and positive total_cost_usd
            usage_resp = client.get(f"/api/v1/jobs/{job_id}/usage")
            assert usage_resp.status_code == 200
            usage = usage_resp.json()["data"]
            assert usage["total_cost_usd"] > 0
            call_types = {b["call_type"] for b in usage["breakdown"]}
            assert "LLM_TEXT" in call_types
            assert "IMAGE_GEN" in call_types

            # 12. Gate 3 single-frame local regeneration on Frame 2 (leaves Frame 1/3 versions intact)
            frame2 = next(f for f in done_detail["frames"] if f["frame_index"] == 2)
            v1_id = frame2["active_version_id"]
            regen_resp = client.post(
                f"/api/v1/frames/{frame2['id']}/regenerate",
                json={"user_edit_note": "Add silver flask prop and warmer sodium light", "cascade_downstream": True},
            )
            assert regen_resp.status_code == 200, regen_resp.text
            regen_data = regen_resp.json()["data"]
            assert regen_data["version"]["version_no"] == 2
            assert regen_data["cascaded_frame_indices"] == [3]
            v2_id = regen_data["version"]["id"]

            after_regen = client.get(f"/api/v1/jobs/{job_id}").json()["data"]
            f2_after = next(f for f in after_regen["frames"] if f["id"] == frame2["id"])
            assert f2_after["active_version_id"] == v2_id
            assert len(after_regen["versions"]) == 4  # f1(v1), f2(v1,v2), f3(v1)

            # Verify dark charcoal trench coat user_ground_truth reached image generation prompt
            assert any("dark charcoal trench coat" in call.get("prompt", "") for call in mock_state.image_calls)

            # 13. Switch active version back to v1
            sw_resp = client.put(
                f"/api/v1/frames/{frame2['id']}/active-version",
                json={"version_id": v1_id},
            )
            assert sw_resp.status_code == 200, sw_resp.text
            assert sw_resp.json()["data"]["active_version_id"] == v1_id
    finally:
        _stop_proc(go_proc)
        _stop_proc(py_proc)
        mock_server.shutdown()
        mock_server.server_close()
        with psycopg.connect(db_url, autocommit=True) as conn:
            if created_thread_id:
                conn.execute("DELETE FROM checkpoints WHERE thread_id = %s", (created_thread_id,))
                conn.execute("DELETE FROM checkpoint_writes WHERE thread_id = %s", (created_thread_id,))
                conn.execute("DELETE FROM checkpoint_blobs WHERE thread_id = %s", (created_thread_id,))
            if created_project_id:
                conn.execute("DELETE FROM projects WHERE id = %s", (created_project_id,))
