"""S3-compatible object storage adapter for RustFS / MinIO / Aliyun OSS (presigned private bucket)."""

from __future__ import annotations

import uuid
from dataclasses import dataclass
from typing import Any

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


@dataclass(frozen=True)
class StoredImage:
    """Metadata describing an uploaded image in private S3 object storage."""

    object_key: str
    bucket: str
    presigned_url: str
    size_bytes: int
    content_type: str = "image/png"


def _normalize_endpoint_url(endpoint: str, use_ssl: bool) -> str:
    cleaned = endpoint.strip().rstrip("/")
    if not cleaned:
        return ""
    if cleaned.startswith(("http://", "https://")):
        return cleaned
    scheme = "https" if use_ssl else "http"
    return f"{scheme}://{cleaned}"


def build_frame_object_key(
    project_id: str,
    job_id: str,
    frame_id: str,
    version_no: int = 1,
    ext: str = "png",
) -> str:
    """Build deterministic hierarchical S3 key for generated storyboard frames."""
    pid = (project_id or "default").strip("/")
    jid = (job_id or "default").strip("/")
    fid = (frame_id or "frame").strip("/")
    suffix = uuid.uuid4().hex[:8]
    return f"projects/{pid}/jobs/{jid}/frames/{fid}/v{version_no}_{suffix}.{ext.lstrip('.')}"


class S3StorageAdapter:
    """Uploads rendered assets to private S3/RustFS buckets and generates presigned URLs."""

    def __init__(
        self,
        endpoint: str,
        bucket: str,
        access_key: str,
        secret_key: str,
        region: str = "us-east-1",
        use_ssl: bool = False,
        presign_expires_sec: int = 3600,
        s3_client: Any = None,
    ) -> None:
        self.endpoint_url = _normalize_endpoint_url(endpoint, use_ssl)
        self.bucket = bucket.strip()
        self.region = region.strip() or "us-east-1"
        self.presign_expires_sec = max(60, int(presign_expires_sec))
        self.client = s3_client or boto3.client(
            "s3",
            endpoint_url=self.endpoint_url,
            region_name=self.region,
            aws_access_key_id=access_key,
            aws_secret_access_key=secret_key,
            use_ssl=use_ssl,
            config=Config(
                signature_version="s3v4",
                s3={"addressing_style": "path"},
            ),
        )

    def ensure_bucket_exists(self) -> None:
        """Verify target bucket exists or create it idempotently."""
        try:
            self.client.head_bucket(Bucket=self.bucket)
        except ClientError as exc:
            err_code = str(exc.response.get("Error", {}).get("Code", ""))
            if err_code in ("404", "NoSuchBucket", "NotFound"):
                self.client.create_bucket(Bucket=self.bucket)
                return
            raise

    def upload_image_bytes(
        self,
        object_key: str,
        data: bytes,
        content_type: str = "image/png",
        expires_in: int | None = None,
    ) -> StoredImage:
        """Upload binary image payload to S3 and return presigned download URL."""
        self.client.put_object(
            Bucket=self.bucket,
            Key=object_key,
            Body=data,
            ContentType=content_type,
        )
        presigned = self.generate_presigned_url(object_key, expires_in=expires_in)
        return StoredImage(
            object_key=object_key,
            bucket=self.bucket,
            presigned_url=presigned,
            size_bytes=len(data),
            content_type=content_type,
        )

    def generate_presigned_url(self, object_key: str, expires_in: int | None = None) -> str:
        """Generate temporary read-only presigned GET URL for private S3 object."""
        ttl = expires_in if expires_in is not None else self.presign_expires_sec
        return self.client.generate_presigned_url(
            "get_object",
            Params={"Bucket": self.bucket, "Key": object_key},
            ExpiresIn=ttl,
        )

    def delete_object(self, object_key: str) -> None:
        """Delete an object by key (primarily for lifecycle cleanup and tests)."""
        self.client.delete_object(Bucket=self.bucket, Key=object_key)
