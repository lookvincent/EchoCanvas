"""Runtime configuration for EchoCanvas Python AI Sidecar."""

from __future__ import annotations

import os
from dataclasses import dataclass
from urllib.parse import urlparse


def _parse_int_env(name: str, default: int) -> int:
    raw = os.getenv(name, "").strip()
    if raw.isdigit():
        return int(raw)
    return default


def _parse_float_env(name: str, default: float) -> float:
    raw = os.getenv(name, "").strip()
    if not raw:
        return default
    try:
        return float(raw)
    except ValueError:
        return default


@dataclass(frozen=True)
class Settings:
    """Service settings loaded from environment variables."""

    database_url: str = ""
    go_gateway_proxy_url: str = "http://localhost:8080/proxy/v1"
    internal_auth_token: str = ""
    py_sidecar_port: int = 8001
    log_level: str = "INFO"
    llm_model: str = "deepseek-chat"
    vision_model: str = "black-forest-labs/FLUX.1-dev"
    vlm_model: str = "gpt-4o-mini"
    s3_endpoint: str = ""
    s3_region: str = "us-east-1"
    s3_access_key: str = ""
    s3_secret_key: str = ""
    s3_bucket: str = "echocanvas-media"
    s3_use_ssl: bool = False
    s3_presign_expires_sec: int = 3600
    vlm_pass_threshold: float = 7.0
    vlm_max_retries: int = 2
    langfuse_host: str = ""
    langfuse_public_key: str = ""
    langfuse_secret_key: str = ""

    @classmethod
    def from_env(cls) -> Settings:
        database_url = os.getenv("DATABASE_URL", "").strip()
        if not database_url:
            pg_host = os.getenv("POSTGRES_HOST", "localhost").strip() or "localhost"
            pg_port = os.getenv("POSTGRES_PORT", "5432").strip() or "5432"
            pg_user = os.getenv("POSTGRES_USER", "").strip()
            pg_pass = os.getenv("POSTGRES_PASSWORD", "").strip()
            pg_db = os.getenv("POSTGRES_DB", "echocanvas").strip() or "echocanvas"
            if pg_user and pg_pass:
                database_url = f"postgres://{pg_user}:{pg_pass}@{pg_host}:{pg_port}/{pg_db}?sslmode=disable"

        proxy_url = os.getenv(
            "GO_GATEWAY_INTERNAL_PROXY_URL",
            "http://localhost:8080/proxy/v1",
        ).strip().rstrip("/")

        port = 8001
        raw_port = os.getenv("PY_SIDECAR_PORT", "").strip()
        if raw_port.isdigit() and int(raw_port) > 0:
            port = int(raw_port)
        else:
            raw_url = os.getenv("PY_SIDECAR_URL", "").strip()
            if raw_url:
                parsed = urlparse(raw_url)
                if parsed.port:
                    port = parsed.port

        return cls(
            database_url=database_url,
            go_gateway_proxy_url=proxy_url or "http://localhost:8080/proxy/v1",
            internal_auth_token=os.getenv("INTERNAL_AUTH_TOKEN", "").strip(),
            py_sidecar_port=port,
            log_level=os.getenv("LOG_LEVEL", "INFO").strip().upper() or "INFO",
            llm_model=os.getenv("LLM_PRIMARY_MODEL", "deepseek-chat").strip() or "deepseek-chat",
            vision_model=(
                os.getenv("VISION_PRIMARY_MODEL", "black-forest-labs/FLUX.1-dev").strip()
                or "black-forest-labs/FLUX.1-dev"
            ),
            vlm_model=os.getenv("VLM_JUDGE_MODEL", "gpt-4o-mini").strip() or "gpt-4o-mini",
            s3_endpoint=os.getenv("S3_ENDPOINT", "").strip(),
            s3_region=os.getenv("S3_REGION", "us-east-1").strip() or "us-east-1",
            s3_access_key=os.getenv("S3_ACCESS_KEY", "").strip(),
            s3_secret_key=os.getenv("S3_SECRET_KEY", "").strip(),
            s3_bucket=os.getenv("S3_BUCKET", "echocanvas-media").strip() or "echocanvas-media",
            s3_use_ssl=os.getenv("S3_USE_SSL", "").strip().lower() == "true",
            s3_presign_expires_sec=max(60, _parse_int_env("S3_PRESIGN_EXPIRES_SEC", 3600)),
            vlm_pass_threshold=_parse_float_env("VLM_PASS_THRESHOLD", 7.0),
            vlm_max_retries=min(2, max(0, _parse_int_env("VLM_MAX_RETRIES", 2))),
            langfuse_host=os.getenv("LANGFUSE_HOST", "").strip().rstrip("/"),
            langfuse_public_key=os.getenv("LANGFUSE_PUBLIC_KEY", "").strip(),
            langfuse_secret_key=os.getenv("LANGFUSE_SECRET_KEY", "").strip(),
        )
