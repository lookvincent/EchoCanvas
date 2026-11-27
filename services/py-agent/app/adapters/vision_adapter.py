"""Flux + IP-Adapter vision adapter routing generation via Go AIGC Gateway and S3 storage."""

from __future__ import annotations

import base64
from dataclasses import dataclass, field
from typing import Any

import httpx

from app.adapters.gateway_client import GatewayClient
from app.adapters.s3_storage import S3StorageAdapter, build_frame_object_key

ASPECT_RATIO_DIMENSIONS: dict[str, tuple[int, int]] = {
    "16:9": (1344, 768),
    "9:16": (768, 1344),
    "1:1": (1024, 1024),
}


@dataclass(frozen=True)
class VisionRenderResult:
    """Normalized output from Flux + IP-Adapter image generation + optional S3 persistence."""

    image_url: str
    object_key: str | None = None
    seed: int | None = None
    width: int = 1344
    height: int = 768
    reference_image_url: str | None = None
    reference_strength: float | None = None
    provider_raw: dict[str, Any] = field(default_factory=dict)


class VisionAdapter:
    """Adapter formatting Flux/IP-Adapter payloads through Go Gateway and persisting images to S3."""

    def __init__(
        self,
        gateway_client: GatewayClient,
        s3_storage: S3StorageAdapter | None = None,
        download_remote_images: bool = False,
    ) -> None:
        self.gateway_client = gateway_client
        self.s3_storage = s3_storage
        self.download_remote_images = download_remote_images

    def render_frame_image(
        self,
        prompt: str,
        job_id: str | None,
        frame_id: str,
        trace_id: str,
        project_id: str = "",
        version_hint: int = 1,
        aspect_ratio: str = "16:9",
        reference_image_url: str | None = None,
        reference_strength: float = 0.65,
        seed: int | None = None,
        negative_prompt: str | None = None,
    ) -> VisionRenderResult:
        width, height = ASPECT_RATIO_DIMENSIONS.get(aspect_ratio, (1344, 768))
        applied_strength = reference_strength if reference_image_url else None

        res = self.gateway_client.generate_image(
            prompt=prompt,
            job_id=job_id,
            frame_id=frame_id,
            trace_id=trace_id,
            aspect_ratio=aspect_ratio,
            reference_image_url=reference_image_url,
            seed=seed,
            width=width,
            height=height,
            ip_adapter_scale=applied_strength,
            negative_prompt=negative_prompt,
        )

        final_url = str(res.get("image_url") or "")
        object_key: str | None = None
        b64_data = str(res.get("b64_json") or "").strip()

        if b64_data:
            raw_bytes = base64.b64decode(b64_data)
            if self.s3_storage is not None:
                key = build_frame_object_key(
                    project_id=project_id,
                    job_id=job_id or "",
                    frame_id=frame_id,
                    version_no=version_hint,
                    ext="png",
                )
                stored = self.s3_storage.upload_image_bytes(key, raw_bytes, content_type="image/png")
                final_url = stored.presigned_url
                object_key = stored.object_key
            elif not final_url:
                final_url = f"data:image/png;base64,{b64_data}"
        elif final_url and self.s3_storage is not None and self.download_remote_images:
            with httpx.Client(trust_env=False, timeout=30.0) as dl_client:
                img_resp = dl_client.get(final_url)
                img_resp.raise_for_status()
                ct = img_resp.headers.get("Content-Type", "image/png").split(";")[0].strip() or "image/png"
                ext = "jpg" if "jpeg" in ct or "jpg" in ct else "png"
                key = build_frame_object_key(
                    project_id=project_id,
                    job_id=job_id or "",
                    frame_id=frame_id,
                    version_no=version_hint,
                    ext=ext,
                )
                stored = self.s3_storage.upload_image_bytes(key, img_resp.content, content_type=ct)
                final_url = stored.presigned_url
                object_key = stored.object_key

        return VisionRenderResult(
            image_url=final_url,
            object_key=object_key,
            seed=res.get("seed"),
            width=width,
            height=height,
            reference_image_url=reference_image_url,
            reference_strength=applied_strength,
            provider_raw=res.get("raw") or {},
        )
