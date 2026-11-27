"""Unit tests for T-M3-02: VLMQualityChecker (retry<=2), TraceRecorder/Langfuse, and eval judge."""

from __future__ import annotations

import base64
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading

import httpx
import pytest
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import MemorySaver

from app.adapters.gateway_client import GatewayClient, SafetyBlockedError
from app.adapters.vision_adapter import VisionRenderResult
from app.config import Settings
from app.main import create_app
from app.observability.tracing import TraceRecorder
from app.quality.vlm_checker import VLMQualityChecker
from app.schemas import AssetCard, EntityStateSnapshot, FrameScript


def _sample_script() -> FrameScript:
    return FrameScript(
        frame_index=2,
        shot_type="Medium Shot",
        camera_angle="Eye-level",
        composition="Subject centered on bridge",
        lighting="Amber sodium haze",
        action="Lin Shen checks watch while holding silver flask",
        dialogue="",
        entities=["hero_linshen"],
    )


def _sample_assets() -> list[AssetCard]:
    return [
        AssetCard(
            asset_type="CHARACTER",
            entity_key="hero_linshen",
            display_name="Lin Shen",
            profile_json={"coat": "dark trench coat"},
            source="user_ground_truth",
            is_locked=True,
        )
    ]


def _sample_states() -> list[EntityStateSnapshot]:
    return [
        EntityStateSnapshot(
            frame_index=1,
            entity_key="hero_linshen",
            state_json={"held_items": "silver flask"},
        )
    ]


def test_vlm_first_pass_and_targeted_retry_success() -> None:
    # Case 1: First-pass success -> 1 render, 1 VLM check
    gw_ok = GatewayClient(
        "http://mock-gw.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(
            lambda req: httpx.Response(
                200,
                json={
                    "choices": [
                        {
                            "message": {
                                "content": json.dumps(
                                    {
                                        "score": 8.9,
                                        "character_consistent": True,
                                        "missing_props": [],
                                        "fix_hint": "",
                                    }
                                )
                            }
                        }
                    ]
                },
            )
        ),
    )
    recorder = TraceRecorder()
    checker_ok = VLMQualityChecker(gw_ok, pass_threshold=7.0, max_retries=2, trace_recorder=recorder)

    prompts_seen: list[str] = []

    def render_fn(prompt: str, attempt: int) -> VisionRenderResult:
        prompts_seen.append(prompt)
        return VisionRenderResult(image_url=f"https://cdn.local/ok_{attempt}.png", seed=100 + attempt)

    out_ok = checker_ok.render_and_verify(
        render_fn=render_fn,
        base_prompt="Lin Shen in dark trench coat",
        frame_script=_sample_script(),
        locked_assets=_sample_assets(),
        prev_entity_states=_sample_states(),
        job_id="job-1",
        frame_id="frame-1",
        trace_id="tr_qa_pass",
        reference_image_url="https://cdn.local/ref.png",
    )
    assert out_ok.passed is True
    assert out_ok.attempts == 1
    assert out_ok.needs_manual_review is False
    assert out_ok.vlm_result.score == 8.9
    spans = recorder.get_spans("tr_qa_pass")
    assert [s.span_name for s in spans] == ["render_frame_image", "vlm_quality_check"]

    # Case 2: Fail attempt 0 (score=5.4), pass attempt 1 (score=8.7) -> fix_hint injected
    vlm_calls = 0

    def fail_then_pass(request: httpx.Request) -> httpx.Response:
        nonlocal vlm_calls
        vlm_calls += 1
        assert request.headers["X-Call-Type"] == "VLM_JUDGE"
        assert request.headers["X-Trace-ID"] == "tr_qa_retry"
        if vlm_calls == 1:
            payload = {
                "score": 5.4,
                "character_consistent": False,
                "missing_props": ["silver flask"],
                "fix_hint": "keep dark trench coat and add silver flask",
            }
        else:
            payload = {
                "score": 8.7,
                "character_consistent": True,
                "missing_props": [],
                "fix_hint": "",
            }
        return httpx.Response(200, json={"choices": [{"message": {"content": json.dumps(payload)}}]})

    gw_retry = GatewayClient(
        "http://mock-gw.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(fail_then_pass),
    )
    retry_prompts: list[str] = []

    def render_retry(prompt: str, attempt: int) -> VisionRenderResult:
        retry_prompts.append(prompt)
        return VisionRenderResult(image_url=f"https://cdn.local/retry_{attempt}.png", seed=200 + attempt)

    out_retry = VLMQualityChecker(gw_retry, pass_threshold=7.0, max_retries=2).render_and_verify(
        render_fn=render_retry,
        base_prompt="Base prompt",
        frame_script=_sample_script(),
        locked_assets=_sample_assets(),
        prev_entity_states=_sample_states(),
        job_id="job-2",
        frame_id="frame-2",
        trace_id="tr_qa_retry",
    )
    assert out_retry.passed is True
    assert out_retry.attempts == 2
    assert len(retry_prompts) == 2
    assert "keep dark trench coat and add silver flask" in retry_prompts[1]
    assert "silver flask" in retry_prompts[1]


def test_vlm_retry_hard_cap_max_2_keeps_best_score_and_safety_halts_immediately() -> None:
    scores = [4.8, 6.4, 5.2, 9.9]  # 4th score must NEVER be reached (max 1 + 2 = 3 total)
    vlm_idx = 0

    def always_low(request: httpx.Request) -> httpx.Response:
        nonlocal vlm_idx
        score = scores[vlm_idx]
        vlm_idx += 1
        return httpx.Response(
            200,
            json={
                "choices": [
                    {
                        "message": {
                            "content": json.dumps(
                                {
                                    "score": score,
                                    "character_consistent": False,
                                    "missing_props": ["coat"],
                                    "fix_hint": f"fix_{vlm_idx}",
                                }
                            )
                        }
                    }
                ]
            },
        )

    gw_low = GatewayClient(
        "http://mock-gw.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(always_low),
    )
    # Even if caller passes max_retries=5, BIZ-02 hard-clamps retries to 2.
    checker = VLMQualityChecker(gw_low, pass_threshold=7.0, max_retries=5)
    assert checker.max_retries == 2

    render_count = 0

    def render_low(prompt: str, attempt: int) -> VisionRenderResult:
        nonlocal render_count
        render_count += 1
        return VisionRenderResult(image_url=f"https://cdn.local/cand_{attempt}.png", seed=attempt)

    out = checker.render_and_verify(
        render_fn=render_low,
        base_prompt="Base prompt",
        frame_script=_sample_script(),
        locked_assets=_sample_assets(),
        prev_entity_states=_sample_states(),
        job_id="job-cap",
        frame_id="frame-cap",
        trace_id="tr_cap",
    )
    assert render_count == 3
    assert vlm_idx == 3
    assert out.passed is False
    assert out.needs_manual_review is True
    assert out.attempts == 3
    assert out.image_url == "https://cdn.local/cand_1.png"  # attempt 1 had highest score 6.4
    assert out.vlm_result.score == 6.4

    # SafetyBlockedError (422) during render must halt at 1 call with 0 retries.
    safety_attempts = 0

    def render_blocked(prompt: str, attempt: int) -> VisionRenderResult:
        nonlocal safety_attempts
        safety_attempts += 1
        raise SafetyBlockedError("SAFETY_BLOCKED", status_code=422)

    with pytest.raises(SafetyBlockedError):
        checker.render_and_verify(
            render_fn=render_blocked,
            base_prompt="Blocked",
            frame_script=_sample_script(),
            locked_assets=_sample_assets(),
            prev_entity_states=_sample_states(),
            job_id="job-sec",
            frame_id="frame-sec",
            trace_id="tr_sec",
        )
    assert safety_attempts == 1


def test_langfuse_ingestion_and_eval_vlm_judge_endpoint() -> None:
    langfuse_batches: list[dict] = []

    def lf_transport(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/public/ingestion"
        langfuse_batches.append(json.loads(request.content.decode("utf-8")))
        return httpx.Response(207, json={"successes": [], "errors": []})

    recorder = TraceRecorder(
        langfuse_host="https://langfuse.mock.local",
        public_key="test_lf_pub",
        secret_key="test_lf_sec",
        transport=httpx.MockTransport(lf_transport),
    )
    with recorder.span("vlm_eval_test", trace_id="tr_lf_123", job_id="job-lf", frame_id="f-lf"):
        pass
    recorder.close()
    assert len(langfuse_batches) == 1
    batch = langfuse_batches[0]["batch"]
    assert batch[0]["body"]["id"] == "tr_lf_123"
    assert batch[1]["body"]["traceId"] == "tr_lf_123"

    # Test /internal/v1/eval/vlm-judge FastAPI endpoint
    judge_json = json.dumps(
        {
            "baseline": {
                "character_consistency": 5.1,
                "prop_scene_continuity": 4.8,
                "script_adherence": 6.5,
                "overall_score": 5.5,
            },
            "echocanvas": {
                "character_consistency": 9.1,
                "prop_scene_continuity": 8.9,
                "script_adherence": 9.0,
                "overall_score": 9.0,
            },
            "winner": "echocanvas",
            "rationale": "Consistent trench coat and umbrella state across frames.",
        }
    )

    def gw_handler(request: httpx.Request) -> httpx.Response:
        assert request.headers["X-Call-Type"] == "VLM_JUDGE"
        assert request.headers["X-Trace-ID"] == "tr_eval_api"
        return httpx.Response(200, json={"choices": [{"message": {"content": judge_json}}]})

    gw = GatewayClient(
        "http://mock-gw.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(gw_handler),
    )
    app = create_app(
        settings=Settings(internal_auth_token="test_internal_token"),
        gateway_client=gw,
        checkpointer=MemorySaver(),
        enable_db_persistence=False,
    )
    with TestClient(app) as client:
        resp = client.post(
            "/internal/v1/eval/vlm-judge",
            headers={"X-Internal-Token": "test_internal_token", "X-Trace-ID": "tr_eval_api"},
            json={
                "story_summary": "Winter overpass narrative",
                "asset_profiles": [{"entity_key": "hero_linshen", "coat": "dark trench coat"}],
                "baseline_images": ["https://cdn.local/b1.png", "https://cdn.local/b2.png"],
                "echocanvas_images": ["https://cdn.local/e1.png", "https://cdn.local/e2.png"],
                "job_id": "job-eval-1",
            },
        )
        assert resp.status_code == 200
        body = resp.json()
        assert body["winner"] == "echocanvas"
        assert body["echocanvas"]["overall_score"] == pytest.approx(9.0)
        assert body["trace_id"] == "tr_eval_api"


def test_vlm_inlines_local_http_image_as_data_uri() -> None:
    png_bytes = base64.b64decode(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
    )

    class _ImgHandler(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args) -> None:
            return

        def do_GET(self) -> None:
            self.send_response(200)
            self.send_header("Content-Type", "image/png")
            self.send_header("Content-Length", str(len(png_bytes)))
            self.end_headers()
            self.wfile.write(png_bytes)

    srv = ThreadingHTTPServer(("127.0.0.1", 0), _ImgHandler)
    port = srv.server_address[1]
    thread = threading.Thread(target=srv.serve_forever, daemon=True)
    thread.start()

    captured_urls: list[str] = []

    def gw_handler(request: httpx.Request) -> httpx.Response:
        payload = json.loads(request.content.decode("utf-8"))
        for msg in payload.get("messages", []):
            if isinstance(msg.get("content"), list):
                for block in msg["content"]:
                    if isinstance(block, dict) and block.get("type") == "image_url":
                        captured_urls.append(str(block["image_url"]["url"]))
        return httpx.Response(
            200,
            json={
                "choices": [
                    {
                        "message": {
                            "content": json.dumps(
                                {
                                    "score": 8.4,
                                    "character_consistent": True,
                                    "missing_props": [],
                                    "fix_hint": "",
                                }
                            )
                        }
                    }
                ]
            },
        )

    try:
        gw = GatewayClient(
            "http://mock-gw.local/proxy/v1",
            internal_token="test_internal_token",
            transport=httpx.MockTransport(gw_handler),
        )
        checker = VLMQualityChecker(gw, vlm_model="deepseek-v4.1-flash")
        res = checker.check_image(
            image_url=f"http://127.0.0.1:{port}/frame1.png",
            frame_script=_sample_script(),
            locked_assets=_sample_assets(),
            prev_entity_states=_sample_states(),
            job_id="job-inline",
            frame_id="frame-inline",
            trace_id="tr_inline",
            reference_image_url="https://cdn.example.com/ref.png",
        )
        assert res.score == pytest.approx(8.4)
        assert len(captured_urls) == 2
        assert captured_urls[0].startswith("data:image/png;base64,")
        assert captured_urls[1] == "https://cdn.example.com/ref.png"
    finally:
        srv.shutdown()
        srv.server_close()
