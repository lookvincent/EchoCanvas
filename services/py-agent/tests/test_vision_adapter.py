"""Unit and live Docker RustFS S3 integration tests for T-M3-01."""

from __future__ import annotations

import base64
import json
import os
import uuid

import httpx
import pytest

from app.adapters.gateway_client import (
    BudgetExceededError,
    GatewayClient,
    SafetyBlockedError,
)
from app.adapters.s3_storage import S3StorageAdapter, build_frame_object_key
from app.adapters.vision_adapter import ASPECT_RATIO_DIMENSIONS, VisionAdapter

# Minimal valid 1x1 transparent PNG bytes.
SAMPLE_PNG_BYTES = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
)


class _FakeS3Client:
    def __init__(self) -> None:
        self.objects: dict[tuple[str, str], bytes] = {}
        self.buckets: set[str] = {"echocanvas-media"}

    def head_bucket(self, Bucket: str) -> dict:
        if Bucket not in self.buckets:
            raise RuntimeError("NoSuchBucket")
        return {}

    def create_bucket(self, Bucket: str) -> dict:
        self.buckets.add(Bucket)
        return {}

    def put_object(self, Bucket: str, Key: str, Body: bytes, ContentType: str = "image/png") -> dict:
        self.objects[(Bucket, Key)] = Body
        return {"ETag": "mock-etag"}

    def generate_presigned_url(self, ClientMethod: str, Params: dict, ExpiresIn: int) -> str:
        return f"https://mock-s3.local/{Params['Bucket']}/{Params['Key']}?X-Amz-Expires={ExpiresIn}"

    def delete_object(self, Bucket: str, Key: str) -> dict:
        self.objects.pop((Bucket, Key), None)
        return {}


def test_aspect_ratio_dimensions_and_flux_ip_adapter_payload() -> None:
    captured_requests: list[tuple[dict, dict]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/proxy/v1/images/generations"
        payload = json.loads(request.content.decode("utf-8"))
        captured_requests.append((dict(request.headers), payload))
        return httpx.Response(
            200,
            json={
                "data": [
                    {
                        "url": "https://mock-cdn.echocanvas.local/flux/out_1.png",
                        "seed": 7788,
                    }
                ]
            },
        )

    gw = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        vision_model="black-forest-labs/FLUX.1-dev",
        transport=httpx.MockTransport(handler),
    )
    adapter = VisionAdapter(gw)

    for ratio, expected_dims in ASPECT_RATIO_DIMENSIONS.items():
        res = adapter.render_frame_image(
            prompt="Lin Shen in dark trench coat on snowy Beijing overpass",
            job_id="job-123",
            frame_id="frame-456",
            trace_id="tr_vision_1",
            aspect_ratio=ratio,
            reference_image_url="https://mock-cdn.echocanvas.local/ref_hero.png",
            reference_strength=0.72,
            negative_prompt="blurry, extra fingers",
        )
        assert (res.width, res.height) == expected_dims
        assert res.image_url == "https://mock-cdn.echocanvas.local/flux/out_1.png"
        assert res.seed == 7788

    headers, first_payload = captured_requests[0]
    assert headers["x-internal-token"] == "test_internal_token"
    assert headers["x-job-id"] == "job-123"
    assert headers["x-frame-id"] == "frame-456"
    assert headers["x-trace-id"] == "tr_vision_1"
    assert headers["x-call-type"] == "IMAGE_GEN"
    assert first_payload["width"] == 1344
    assert first_payload["height"] == 768
    assert first_payload["image_size"] == "1344x768"
    assert first_payload["reference_image_url"] == "https://mock-cdn.echocanvas.local/ref_hero.png"
    assert first_payload["ip_adapter_scale"] == pytest.approx(0.72)
    assert first_payload["negative_prompt"] == "blurry, extra fingers"


def test_b64_json_upload_to_s3_and_error_propagation() -> None:
    b64_str = base64.b64encode(SAMPLE_PNG_BYTES).decode("ascii")

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"data": [{"b64_json": b64_str, "seed": 9901}]})

    fake_client = _FakeS3Client()
    storage = S3StorageAdapter(
        endpoint="localhost:9000",
        bucket="echocanvas-media",
        access_key="test_access",
        secret_key="test_secret",
        presign_expires_sec=1800,
        s3_client=fake_client,
    )
    gw = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(handler),
    )
    adapter = VisionAdapter(gw, s3_storage=storage)

    key_check = build_frame_object_key("p1", "j1", "f1", version_no=2)
    assert key_check.startswith("projects/p1/jobs/j1/frames/f1/v2_")

    res = adapter.render_frame_image(
        prompt="Close up portrait",
        job_id="j1",
        frame_id="f1",
        trace_id="tr_b64",
        project_id="p1",
        version_hint=2,
        aspect_ratio="1:1",
    )
    assert res.object_key is not None
    assert "X-Amz-Expires=1800" in res.image_url
    assert fake_client.objects[("echocanvas-media", res.object_key)] == SAMPLE_PNG_BYTES

    gw_422 = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(lambda req: httpx.Response(422, json={"code": 42201})),
    )
    with pytest.raises(SafetyBlockedError):
        VisionAdapter(gw_422).render_frame_image("unsafe", "j1", "f1", "tr_422")

    gw_402 = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(lambda req: httpx.Response(402, json={"code": 40201})),
    )
    with pytest.raises(BudgetExceededError):
        VisionAdapter(gw_402).render_frame_image("expensive", "j1", "f1", "tr_402")


@pytest.mark.skipif(
    not (os.getenv("S3_ENDPOINT") and os.getenv("S3_ACCESS_KEY") and os.getenv("S3_SECRET_KEY")),
    reason="S3_ENDPOINT / S3_ACCESS_KEY / S3_SECRET_KEY not set for live RustFS test",
)
def test_live_rustfs_s3_presigned_upload_and_download() -> None:
    storage = S3StorageAdapter(
        endpoint=os.environ["S3_ENDPOINT"],
        bucket=os.getenv("S3_BUCKET", "echocanvas-media"),
        access_key=os.environ["S3_ACCESS_KEY"],
        secret_key=os.environ["S3_SECRET_KEY"],
        region=os.getenv("S3_REGION", "us-east-1"),
        use_ssl=os.getenv("S3_USE_SSL", "false").lower() == "true",
        presign_expires_sec=600,
    )
    storage.ensure_bucket_exists()

    b64_str = base64.b64encode(SAMPLE_PNG_BYTES).decode("ascii")
    gw = GatewayClient(
        "http://mock-gateway.local/proxy/v1",
        internal_token="test_internal_token",
        transport=httpx.MockTransport(
            lambda req: httpx.Response(200, json={"data": [{"b64_json": b64_str, "seed": 2026}]})
        ),
    )
    adapter = VisionAdapter(gw, s3_storage=storage)

    uploaded_key: str | None = None
    try:
        res = adapter.render_frame_image(
            prompt="Live RustFS integration frame",
            job_id=f"job_{uuid.uuid4().hex[:8]}",
            frame_id=f"frame_{uuid.uuid4().hex[:8]}",
            trace_id="tr_rustfs_live",
            project_id="proj_rustfs",
            version_hint=1,
            aspect_ratio="16:9",
        )
        uploaded_key = res.object_key
        assert uploaded_key is not None
        assert res.image_url.startswith(("http://", "https://"))

        with httpx.Client(trust_env=False, timeout=10.0) as client:
            dl = client.get(res.image_url)
            assert dl.status_code == 200
            assert dl.content == SAMPLE_PNG_BYTES
    finally:
        if uploaded_key:
            storage.delete_object(uploaded_key)
