"""HTTP client for calling Go AIGC Gateway (/proxy/v1/*) per ADR-04."""

from __future__ import annotations

import json
import re
from typing import Any, TypeVar

import httpx
from pydantic import BaseModel, ValidationError

TModel = TypeVar("TModel", bound=BaseModel)


class GatewayError(RuntimeError):
    """Base error raised when Go AIGC Gateway returns an error response."""

    def __init__(self, message: str, status_code: int = 500) -> None:
        super().__init__(message)
        self.status_code = status_code


class BudgetExceededError(GatewayError):
    """Raised when Go Gateway rejects a call due to PAUSED_BUDGET (HTTP 402)."""


class SafetyBlockedError(GatewayError):
    """Raised when upstream moderation blocks content (HTTP 422)."""


class UpstreamUnavailableError(GatewayError):
    """Raised when upstream model providers are unavailable or rate-limited (429/5xx)."""


def _strip_json_fences(raw_text: str) -> str:
    """Remove markdown code fences and isolate outer JSON object/array if wrapped."""
    cleaned = raw_text.strip()
    fence_match = re.search(r"```(?:json)?\s*([\s\S]*?)\s*```", cleaned, re.IGNORECASE)
    if fence_match:
        cleaned = fence_match.group(1).strip()

    if not cleaned.startswith(("{", "[")):
        obj_start = cleaned.find("{")
        obj_end = cleaned.rfind("}")
        if obj_start != -1 and obj_end > obj_start:
            cleaned = cleaned[obj_start : obj_end + 1]
    return cleaned


def _extract_chat_content(payload: Any) -> str:
    """Extract assistant text from OpenAI-compatible chat completion JSON."""
    if isinstance(payload, dict) and "choices" in payload:
        choices = payload.get("choices") or []
        if not choices:
            raise ValueError("empty choices in chat completion response")
        msg = choices[0].get("message") or {}
        content = msg.get("content")
        if isinstance(content, str):
            return content
        if isinstance(content, list):
            parts = [
                str(block.get("text", ""))
                for block in content
                if isinstance(block, dict) and block.get("type") in (None, "text")
            ]
            return "\n".join(parts)
    if isinstance(payload, str):
        return payload
    raise ValueError(f"unsupported chat completion payload shape: {type(payload).__name__}")


class GatewayClient:
    """Centralized client routing all LLM/Vision calls through Go AIGC Gateway."""

    def __init__(
        self,
        proxy_base_url: str,
        internal_token: str = "",
        llm_model: str = "deepseek-chat",
        vision_model: str = "black-forest-labs/FLUX.1-dev",
        timeout_sec: float = 90.0,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        self.proxy_base_url = proxy_base_url.rstrip("/")
        self.internal_token = internal_token.strip()
        self.llm_model = llm_model
        self.vision_model = vision_model
        self._client = httpx.Client(timeout=timeout_sec, transport=transport)

    def close(self) -> None:
        self._client.close()

    def _build_headers(
        self,
        job_id: str | None,
        trace_id: str,
        call_type: str,
        frame_id: str | None = None,
    ) -> dict[str, str]:
        headers = {
            "Content-Type": "application/json",
            "X-Trace-ID": trace_id,
            "X-Call-Type": call_type,
        }
        if self.internal_token:
            headers["X-Internal-Token"] = self.internal_token
        if job_id:
            headers["X-Job-ID"] = job_id
        if frame_id:
            headers["X-Frame-ID"] = frame_id
        return headers

    def _raise_for_gateway_status(self, resp: httpx.Response) -> None:
        if resp.status_code < 400:
            return
        detail = resp.text[:400]
        if resp.status_code == 402:
            raise BudgetExceededError(f"job budget exceeded: {detail}", status_code=402)
        if resp.status_code == 422:
            raise SafetyBlockedError(f"SAFETY_BLOCKED: {detail}", status_code=422)
        if resp.status_code == 429 or resp.status_code >= 500:
            raise UpstreamUnavailableError(
                f"upstream gateway unavailable ({resp.status_code}): {detail}",
                status_code=resp.status_code,
            )
        raise GatewayError(f"gateway error ({resp.status_code}): {detail}", status_code=resp.status_code)

    def chat_completions(
        self,
        messages: list[dict[str, str]],
        job_id: str | None,
        trace_id: str,
        frame_id: str | None = None,
        call_type: str = "LLM_TEXT",
        temperature: float = 0.2,
        response_format: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        """Call Go Gateway POST /proxy/v1/chat/completions."""
        body: dict[str, Any] = {
            "model": self.llm_model,
            "messages": messages,
            "temperature": temperature,
        }
        if response_format is not None:
            body["response_format"] = response_format

        resp = self._client.post(
            f"{self.proxy_base_url}/chat/completions",
            headers=self._build_headers(job_id, trace_id, call_type, frame_id=frame_id),
            json=body,
        )
        self._raise_for_gateway_status(resp)
        return resp.json()

    def chat_json(
        self,
        messages: list[dict[str, str]],
        schema_cls: type[TModel],
        job_id: str | None,
        trace_id: str,
        frame_id: str | None = None,
        call_type: str = "LLM_TEXT",
        temperature: float = 0.2,
    ) -> TModel:
        """Call chat_completions and parse/validate output into a Pydantic model, with 1 repair retry."""
        raw_resp = self.chat_completions(
            messages=messages,
            job_id=job_id,
            trace_id=trace_id,
            frame_id=frame_id,
            call_type=call_type,
            temperature=temperature,
            response_format={"type": "json_object"},
        )
        raw_text = _extract_chat_content(raw_resp)
        cleaned = _strip_json_fences(raw_text)
        try:
            return schema_cls.model_validate_json(cleaned)
        except (ValidationError, ValueError, TypeError) as first_err:
            repair_messages = [
                *messages,
                {"role": "assistant", "content": raw_text},
                {
                    "role": "user",
                    "content": (
                        f"Your previous output could not be validated ({first_err}). "
                        f"Return ONLY valid raw JSON conforming to {schema_cls.__name__} "
                        f"with schema {json.dumps(schema_cls.model_json_schema(), ensure_ascii=False)}. "
                        "Do not include markdown code fences or prose."
                    ),
                },
            ]
            repaired_resp = self.chat_completions(
                messages=repair_messages,
                job_id=job_id,
                trace_id=trace_id,
                frame_id=frame_id,
                call_type=call_type,
                temperature=0.0,
                response_format={"type": "json_object"},
            )
            repaired_text = _extract_chat_content(repaired_resp)
            repaired_clean = _strip_json_fences(repaired_text)
            return schema_cls.model_validate_json(repaired_clean)

    def generate_image(
        self,
        prompt: str,
        job_id: str | None,
        frame_id: str,
        trace_id: str,
        aspect_ratio: str = "16:9",
        reference_image_url: str | None = None,
        seed: int | None = None,
    ) -> dict[str, Any]:
        """Call Go Gateway POST /proxy/v1/images/generations."""
        payload: dict[str, Any] = {
            "model": self.vision_model,
            "prompt": prompt,
            "n": 1,
            "aspect_ratio": aspect_ratio,
        }
        if reference_image_url:
            payload["reference_image_url"] = reference_image_url
        if seed is not None:
            payload["seed"] = seed

        resp = self._client.post(
            f"{self.proxy_base_url}/images/generations",
            headers=self._build_headers(job_id, trace_id, "IMAGE_GEN", frame_id=frame_id),
            json=payload,
        )
        self._raise_for_gateway_status(resp)
        data = resp.json()

        image_url = ""
        resolved_seed = seed
        if isinstance(data.get("data"), list) and data["data"]:
            first = data["data"][0]
            image_url = str(first.get("url") or "")
            if first.get("seed") is not None:
                resolved_seed = int(first["seed"])
        elif isinstance(data.get("images"), list) and data["images"]:
            first = data["images"][0]
            if isinstance(first, dict):
                image_url = str(first.get("url") or "")
                if first.get("seed") is not None:
                    resolved_seed = int(first["seed"])
            elif isinstance(first, str):
                image_url = first
        elif data.get("image_url"):
            image_url = str(data["image_url"])

        if not image_url:
            raise GatewayError("image generation response missing image URL", status_code=502)

        if resolved_seed is None and data.get("seed") is not None:
            resolved_seed = int(data["seed"])

        return {
            "image_url": image_url,
            "seed": resolved_seed,
            "raw": data,
        }
