"""VLM automatic quality inspection (retry <= 2 per BIZ-02) and Eval comparator."""

from __future__ import annotations

import base64
import ipaddress
import json
import logging
from contextlib import nullcontext
from dataclasses import dataclass
from typing import Any, Callable
from urllib.parse import urlparse

import httpx

from app.adapters.gateway_client import (
    BudgetExceededError,
    GatewayClient,
    SafetyBlockedError,
)
from app.observability.tracing import TraceRecorder
from app.schemas import (
    AssetCard,
    EntityStateSnapshot,
    FrameScript,
    VLMCheckResult,
    VLMJudgeCompareRequest,
    VLMJudgeCompareResponse,
)

logger = logging.getLogger("echocanvas.vlm_checker")


@dataclass(frozen=True)
class VerifiedRenderOutcome:
    """Final image candidate selected by VLMQualityChecker after QA attempts."""

    image_url: str
    seed: int | None
    prompt_snapshot: str
    vlm_result: VLMCheckResult
    passed: bool
    needs_manual_review: bool
    attempts: int
    object_key: str | None = None


_LOCAL_IMAGE_HOSTS = frozenset(
    {"localhost", "127.0.0.1", "::1", "rustfs", "minio", "host.docker.internal"}
)


def _is_local_or_private_host(hostname: str) -> bool:
    host = hostname.strip().lower().rstrip(".")
    if not host:
        return False
    if host in _LOCAL_IMAGE_HOSTS:
        return True
    try:
        ip = ipaddress.ip_address(host)
        return ip.is_loopback or ip.is_private
    except ValueError:
        return False


def _resolve_image_url_for_vlm(url: str) -> str:
    """Inline local/private HTTP images (e.g., RustFS presigned URLs) as base64 data URIs for remote VLMs."""
    if not url or url.startswith("data:"):
        return url
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https") or not _is_local_or_private_host(
        parsed.hostname or ""
    ):
        return url
    try:
        resp = httpx.get(url, timeout=15.0, trust_env=False)
        if resp.status_code == 200 and resp.content and len(resp.content) <= 3_000_000:
            content_type = (
                resp.headers.get("content-type", "image/png").split(";")[0].strip()
                or "image/png"
            )
            b64 = base64.b64encode(resp.content).decode("ascii")
            return f"data:{content_type};base64,{b64}"
    except Exception as exc:  # pragma: no cover - best-effort fallback
        logger.debug("Failed to inline local image URL %s for VLM: %s", url, exc)
    return url


def _build_targeted_retry_prompt(base_prompt: str, result: VLMCheckResult) -> str:
    corrections: list[str] = []
    if result.fix_hint.strip():
        corrections.append(result.fix_hint.strip())
    if result.missing_props:
        corrections.append("must clearly show props/details: " + ", ".join(result.missing_props))
    if not corrections:
        corrections.append("strictly preserve character appearance and scene continuity")
    return f"{base_prompt}\n[QA Correction] {'; '.join(corrections)}"


def _extract_render_fields(render_res: Any) -> tuple[str, int | None, str | None]:
    if isinstance(render_res, dict):
        return (
            str(render_res.get("image_url") or ""),
            render_res.get("seed"),
            render_res.get("object_key"),
        )
    return (
        str(getattr(render_res, "image_url", "")),
        getattr(render_res, "seed", None),
        getattr(render_res, "object_key", None),
    )


class VLMQualityChecker:
    """Runs VLM visual QA via Go Gateway with hard retry cap <= 2 and best-score retention."""

    def __init__(
        self,
        gateway_client: GatewayClient,
        pass_threshold: float = 7.0,
        max_retries: int = 2,
        vlm_model: str = "gpt-4o-mini",
        trace_recorder: TraceRecorder | None = None,
    ) -> None:
        self.gateway_client = gateway_client
        self.pass_threshold = float(pass_threshold)
        # Hard cap auto-retries to <= 2 per BIZ-02 / FR-D03.
        self.max_retries = min(max(0, int(max_retries)), 2)
        self.vlm_model = vlm_model
        self.trace_recorder = trace_recorder

    def _span(
        self,
        name: str,
        trace_id: str,
        job_id: str | None,
        frame_id: str,
        metadata: dict[str, Any] | None = None,
    ):
        if self.trace_recorder is None:
            return nullcontext({})
        return self.trace_recorder.span(
            span_name=name,
            trace_id=trace_id,
            job_id=job_id or "",
            frame_id=frame_id,
            metadata=metadata,
        )

    def check_image(
        self,
        image_url: str,
        frame_script: FrameScript,
        locked_assets: list[AssetCard],
        prev_entity_states: list[EntityStateSnapshot],
        job_id: str | None,
        frame_id: str,
        trace_id: str,
        reference_image_url: str | None = None,
    ) -> VLMCheckResult:
        """Inspect a rendered storyboard image against character traits and frame continuity."""
        locked_summary = json.dumps(
            [
                {
                    "entity_key": a.entity_key,
                    "display_name": a.display_name,
                    "profile": a.profile_json,
                }
                for a in locked_assets
            ],
            ensure_ascii=False,
        )
        prev_summary = json.dumps(
            [
                {"entity_key": s.entity_key, "state": s.state_json}
                for s in prev_entity_states
            ],
            ensure_ascii=False,
        )
        instruction = (
            "VLM_QUALITY_CHECK: Evaluate this storyboard frame for character consistency, "
            "expected props/apparel continuity, and anatomy/artifact quality.\n"
            f"Frame Action: {frame_script.action} (shot={frame_script.shot_type}, lighting={frame_script.lighting})\n"
            f"Locked Assets: {locked_summary}\n"
            f"Inherited Entity States: {prev_summary}\n"
            'Return strict JSON: {"score": 0..10, "character_consistent": true|false, '
            '"missing_props": [...], "fix_hint": "..."}'
        )

        resolved_image_url = _resolve_image_url_for_vlm(image_url)
        user_content: list[dict[str, Any]] = [
            {"type": "text", "text": instruction},
            {"type": "image_url", "image_url": {"url": resolved_image_url}},
        ]
        if reference_image_url:
            resolved_ref_url = _resolve_image_url_for_vlm(reference_image_url)
            user_content.append(
                {"type": "text", "text": "Reference character anchor image:"}
            )
            user_content.append(
                {"type": "image_url", "image_url": {"url": resolved_ref_url}}
            )

        return self.gateway_client.chat_json(
            messages=[
                {
                    "role": "system",
                    "content": "You are a strict VLM visual continuity QA inspector returning valid JSON only.",
                },
                {"role": "user", "content": user_content},
            ],
            schema_cls=VLMCheckResult,
            job_id=job_id,
            frame_id=frame_id,
            trace_id=trace_id,
            call_type="VLM_JUDGE",
            model_override=self.vlm_model,
        )

    def render_and_verify(
        self,
        render_fn: Callable[[str, int], Any],
        base_prompt: str,
        frame_script: FrameScript,
        locked_assets: list[AssetCard],
        prev_entity_states: list[EntityStateSnapshot],
        job_id: str | None,
        frame_id: str,
        trace_id: str,
        reference_image_url: str | None = None,
    ) -> VerifiedRenderOutcome:
        """Execute render + VLM QA loop with up to max_retries (<=2) targeted corrections."""
        current_prompt = base_prompt
        best_candidate: dict[str, Any] | None = None

        for attempt in range(self.max_retries + 1):
            with self._span(
                "render_frame_image",
                trace_id=trace_id,
                job_id=job_id,
                frame_id=frame_id,
                metadata={"attempt": attempt + 1},
            ):
                try:
                    render_res = render_fn(current_prompt, attempt)
                except (SafetyBlockedError, BudgetExceededError):
                    # BIZ-02 / FR-D04: never retry safety blocks or budget stops.
                    raise

            img_url, seed, object_key = _extract_render_fields(render_res)

            with self._span(
                "vlm_quality_check",
                trace_id=trace_id,
                job_id=job_id,
                frame_id=frame_id,
                metadata={"attempt": attempt + 1},
            ):
                check = self.check_image(
                    image_url=img_url,
                    frame_script=frame_script,
                    locked_assets=locked_assets,
                    prev_entity_states=prev_entity_states,
                    job_id=job_id,
                    frame_id=frame_id,
                    trace_id=trace_id,
                    reference_image_url=reference_image_url,
                )

            if (
                best_candidate is None
                or check.score > best_candidate["vlm_result"].score
            ):
                best_candidate = {
                    "image_url": img_url,
                    "seed": seed,
                    "object_key": object_key,
                    "prompt_snapshot": current_prompt,
                    "vlm_result": check,
                }

            if check.score >= self.pass_threshold and check.character_consistent:
                enriched = check.model_copy(
                    update={"needs_manual_review": False, "attempts": attempt + 1}
                )
                return VerifiedRenderOutcome(
                    image_url=img_url,
                    seed=seed,
                    object_key=object_key,
                    prompt_snapshot=current_prompt,
                    vlm_result=enriched,
                    passed=True,
                    needs_manual_review=False,
                    attempts=attempt + 1,
                )

            if attempt < self.max_retries:
                logger.info(
                    "VLM QA score=%.2f consistent=%s below threshold %.2f (frame=%s attempt=%d); retrying with hint=%r",
                    check.score,
                    check.character_consistent,
                    self.pass_threshold,
                    frame_id,
                    attempt + 1,
                    check.fix_hint,
                )
                current_prompt = _build_targeted_retry_prompt(base_prompt, check)

        assert best_candidate is not None
        total_attempts = self.max_retries + 1
        best_res = best_candidate["vlm_result"].model_copy(
            update={"needs_manual_review": True, "attempts": total_attempts}
        )
        return VerifiedRenderOutcome(
            image_url=best_candidate["image_url"],
            seed=best_candidate["seed"],
            object_key=best_candidate["object_key"],
            prompt_snapshot=best_candidate["prompt_snapshot"],
            vlm_result=best_res,
            passed=False,
            needs_manual_review=True,
            attempts=total_attempts,
        )

    def judge_comparison(
        self,
        req: VLMJudgeCompareRequest,
        trace_id: str,
    ) -> VLMJudgeCompareResponse:
        """Run VLM-as-a-Judge comparative scoring between Baseline and EchoCanvas sequences."""
        with self._span(
            "vlm_eval_judge",
            trace_id=trace_id,
            job_id=req.job_id,
            frame_id="eval",
        ):
            assets_json = json.dumps(req.asset_profiles, ensure_ascii=False)
            content: list[dict[str, Any]] = [
                {
                    "type": "text",
                    "text": (
                        "VLM_EVAL_JUDGE: Compare Baseline sequence vs EchoCanvas sequence for "
                        "character_consistency (0-10), prop_scene_continuity (0-10), script_adherence (0-10), "
                        "and overall_score (0-10).\n"
                        f"Story: {req.story_summary}\nLocked Assets: {assets_json}\n"
                        'Return JSON matching {"baseline": {...}, "echocanvas": {...}, '
                        '"winner": "baseline|echocanvas|tie", "rationale": "..."}'
                    ),
                },
                {"type": "text", "text": "Baseline frames:"},
            ]
            for url in req.baseline_images:
                content.append({"type": "image_url", "image_url": {"url": _resolve_image_url_for_vlm(url)}})
            content.append({"type": "text", "text": "EchoCanvas frames:"})
            for url in req.echocanvas_images:
                content.append({"type": "image_url", "image_url": {"url": _resolve_image_url_for_vlm(url)}})

            res = self.gateway_client.chat_json(
                messages=[
                    {
                        "role": "system",
                        "content": "You are an impartial multimodal VLM-as-a-Judge evaluator returning strict JSON.",
                    },
                    {"role": "user", "content": content},
                ],
                schema_cls=VLMJudgeCompareResponse,
                job_id=req.job_id,
                trace_id=trace_id,
                call_type="VLM_JUDGE",
                model_override=self.vlm_model,
            )
            return res.model_copy(update={"trace_id": trace_id})
