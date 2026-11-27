"""Model gateway and external storage adapters."""

from app.adapters.gateway_client import (
    BudgetExceededError,
    GatewayClient,
    GatewayError,
    SafetyBlockedError,
    UpstreamUnavailableError,
)
from app.adapters.s3_storage import S3StorageAdapter, StoredImage, build_frame_object_key
from app.adapters.vision_adapter import ASPECT_RATIO_DIMENSIONS, VisionAdapter, VisionRenderResult

__all__ = [
    "ASPECT_RATIO_DIMENSIONS",
    "BudgetExceededError",
    "GatewayClient",
    "GatewayError",
    "S3StorageAdapter",
    "SafetyBlockedError",
    "StoredImage",
    "UpstreamUnavailableError",
    "VisionAdapter",
    "VisionRenderResult",
    "build_frame_object_key",
]
