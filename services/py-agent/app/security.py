"""Internal authentication and trace-ID middleware helpers."""

from __future__ import annotations

import hmac
import uuid

from fastapi import Header, HTTPException, Request


def verify_internal_token(
    request: Request,
    x_internal_token: str | None = Header(default=None, alias="X-Internal-Token"),
) -> str:
    """Validate X-Internal-Token with constant-time comparison; fail closed if unconfigured."""
    expected = (getattr(request.app.state.settings, "internal_auth_token", "") or "").strip()
    provided = (x_internal_token or "").strip()
    if not expected or not provided or not hmac.compare_digest(provided, expected):
        raise HTTPException(status_code=401, detail="unauthorized internal token")
    return provided


def get_trace_id(
    x_trace_id: str | None = Header(default=None, alias="X-Trace-ID"),
) -> str:
    """Propagate incoming X-Trace-ID or generate a fresh trace identifier."""
    cleaned = (x_trace_id or "").strip()
    if cleaned:
        return cleaned
    return f"tr_{uuid.uuid4().hex[:16]}"
