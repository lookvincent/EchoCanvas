"""Cross-service X-Trace-ID span recorder with optional best-effort Langfuse ingestion."""

from __future__ import annotations

import logging
import threading
import time
import uuid
from collections import deque
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Iterator

import httpx

logger = logging.getLogger("echocanvas.observability")


@dataclass(frozen=True)
class TraceSpan:
    """In-memory span record aligned with EchoCanvas trace_id correlation."""

    trace_id: str
    span_name: str
    job_id: str = ""
    frame_id: str = ""
    metadata: dict[str, Any] = field(default_factory=dict)
    started_at: str = ""
    ended_at: str = ""
    duration_ms: int = 0
    status: str = "ok"
    error: str | None = None


class TraceRecorder:
    """Records spans keyed by X-Trace-ID locally and optionally forwards to Langfuse."""

    def __init__(
        self,
        langfuse_host: str = "",
        public_key: str = "",
        secret_key: str = "",
        transport: httpx.BaseTransport | None = None,
        max_history: int = 500,
    ) -> None:
        self.langfuse_host = langfuse_host.strip().rstrip("/")
        self.public_key = public_key.strip()
        self.secret_key = secret_key.strip()
        self.langfuse_enabled = bool(self.langfuse_host and self.public_key and self.secret_key)
        self._lock = threading.Lock()
        self._spans: deque[TraceSpan] = deque(maxlen=max_history)
        self._client: httpx.Client | None = (
            httpx.Client(timeout=2.0, transport=transport, trust_env=False)
            if (self.langfuse_enabled or transport is not None)
            else None
        )

    def close(self) -> None:
        if self._client is not None:
            self._client.close()
            self._client = None

    @contextmanager
    def span(
        self,
        span_name: str,
        trace_id: str,
        job_id: str = "",
        frame_id: str = "",
        metadata: dict[str, Any] | None = None,
    ) -> Iterator[dict[str, Any]]:
        """Context manager timing an execution span under X-Trace-ID."""
        span_meta = dict(metadata or {})
        started_dt = datetime.now(timezone.utc)
        t0 = time.monotonic()
        status = "ok"
        err_msg: str | None = None
        try:
            yield span_meta
        except Exception as exc:
            status = "error"
            err_msg = str(exc)
            raise
        finally:
            ended_dt = datetime.now(timezone.utc)
            duration_ms = max(0, int((time.monotonic() - t0) * 1000))
            rec = TraceSpan(
                trace_id=trace_id,
                span_name=span_name,
                job_id=job_id,
                frame_id=frame_id,
                metadata=span_meta,
                started_at=started_dt.isoformat(),
                ended_at=ended_dt.isoformat(),
                duration_ms=duration_ms,
                status=status,
                error=err_msg,
            )
            with self._lock:
                self._spans.append(rec)
            self._emit_langfuse(rec)

    def get_spans(self, trace_id: str | None = None) -> list[TraceSpan]:
        """Return captured spans, optionally filtered by trace_id."""
        with self._lock:
            if not trace_id:
                return list(self._spans)
            return [s for s in self._spans if s.trace_id == trace_id]

    def _emit_langfuse(self, rec: TraceSpan) -> None:
        if not self.langfuse_enabled or self._client is None:
            return
        payload = {
            "batch": [
                {
                    "id": str(uuid.uuid4()),
                    "timestamp": rec.started_at,
                    "type": "trace-create",
                    "body": {
                        "id": rec.trace_id,
                        "name": "echocanvas.workflow",
                        "metadata": {"job_id": rec.job_id},
                    },
                },
                {
                    "id": str(uuid.uuid4()),
                    "timestamp": rec.ended_at,
                    "type": "span-create",
                    "body": {
                        "id": str(uuid.uuid4()),
                        "traceId": rec.trace_id,
                        "name": rec.span_name,
                        "startTime": rec.started_at,
                        "endTime": rec.ended_at,
                        "level": "ERROR" if rec.status == "error" else "DEFAULT",
                        "statusMessage": rec.error,
                        "metadata": {
                            "job_id": rec.job_id,
                            "frame_id": rec.frame_id,
                            "duration_ms": rec.duration_ms,
                            **rec.metadata,
                        },
                    },
                },
            ]
        }
        try:
            self._client.post(
                f"{self.langfuse_host}/api/public/ingestion",
                auth=(self.public_key, self.secret_key),
                json=payload,
            )
        except Exception as exc:
            logger.debug("langfuse ingestion best-effort send skipped: %s", exc)
