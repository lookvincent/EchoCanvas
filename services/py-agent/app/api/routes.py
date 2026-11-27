"""Internal HTTP/JSON endpoints (/internal/v1/*) consumed exclusively by Go Control Plane."""

from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException, Request

from app.adapters.gateway_client import (
    BudgetExceededError,
    GatewayError,
    SafetyBlockedError,
    UpstreamUnavailableError,
)
from app.memory.working_context import WorkingContextBuilder
from app.schemas import (
    FrameVersionResponse,
    MemoryRetrieveRequest,
    MemoryRetrieveResponse,
    ParseStoryRequest,
    ParseStoryResponse,
    RegenerateFrameRequest,
    RenderFrameRequest,
    ResumeWorkflowRequest,
    ResumeWorkflowResponse,
)
from app.security import get_trace_id, verify_internal_token
from app.services.workflow_service import WorkflowConflictError, WorkflowService

router = APIRouter(
    prefix="/internal/v1",
    dependencies=[Depends(verify_internal_token)],
)


def _translate_error(exc: Exception) -> HTTPException:
    if isinstance(exc, BudgetExceededError):
        return HTTPException(status_code=402, detail=str(exc))
    if isinstance(exc, SafetyBlockedError):
        return HTTPException(status_code=422, detail=str(exc))
    if isinstance(exc, UpstreamUnavailableError):
        return HTTPException(status_code=502, detail=str(exc))
    if isinstance(exc, GatewayError):
        return HTTPException(status_code=exc.status_code, detail=str(exc))
    if isinstance(exc, LookupError):
        return HTTPException(status_code=404, detail=str(exc))
    if isinstance(exc, WorkflowConflictError):
        return HTTPException(status_code=409, detail=str(exc))
    if isinstance(exc, ValueError):
        return HTTPException(status_code=400, detail=str(exc))
    return HTTPException(status_code=500, detail=str(exc))


@router.post("/workflows/parse-story", response_model=ParseStoryResponse)
def parse_story(
    payload: ParseStoryRequest,
    request: Request,
    trace_id: str = Depends(get_trace_id),
) -> ParseStoryResponse:
    svc: WorkflowService = request.app.state.workflow_service
    try:
        return svc.parse_story(payload, trace_id)
    except Exception as exc:
        raise _translate_error(exc) from exc


@router.post("/workflows/resume", response_model=ResumeWorkflowResponse)
def resume_workflow(
    payload: ResumeWorkflowRequest,
    request: Request,
    trace_id: str = Depends(get_trace_id),
) -> ResumeWorkflowResponse:
    svc: WorkflowService = request.app.state.workflow_service
    try:
        return svc.resume_workflow(payload, trace_id)
    except Exception as exc:
        raise _translate_error(exc) from exc


@router.post("/frames/{frame_id}/render", response_model=FrameVersionResponse)
def render_frame(
    frame_id: str,
    payload: RenderFrameRequest,
    request: Request,
    trace_id: str = Depends(get_trace_id),
) -> FrameVersionResponse:
    svc: WorkflowService = request.app.state.workflow_service
    try:
        return svc.render_frame(frame_id, payload, trace_id)
    except Exception as exc:
        raise _translate_error(exc) from exc


@router.post("/frames/{frame_id}/regenerate", response_model=FrameVersionResponse)
def regenerate_frame(
    frame_id: str,
    payload: RegenerateFrameRequest,
    request: Request,
    trace_id: str = Depends(get_trace_id),
) -> FrameVersionResponse:
    svc: WorkflowService = request.app.state.workflow_service
    try:
        return svc.regenerate_frame(frame_id, payload, trace_id)
    except Exception as exc:
        raise _translate_error(exc) from exc


@router.post("/memory/retrieve", response_model=MemoryRetrieveResponse)
def retrieve_memory(
    payload: MemoryRetrieveRequest,
    request: Request,
) -> MemoryRetrieveResponse:
    builder: WorkingContextBuilder = request.app.state.context_builder
    return builder.build(
        project_id=payload.project_id,
        job_id=payload.job_id,
        frame_index=payload.frame_index,
        frame_script=payload.frame_script,
        query_text=payload.query_text,
        style_preset=payload.style_preset,
        max_prompt_chars=payload.max_prompt_chars,
        top_k=payload.top_k,
    )
