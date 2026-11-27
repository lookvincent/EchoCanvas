"""Pydantic v2 contracts aligned with docs/03-TDD.md and PG16 schema."""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, Field


class AssetCard(BaseModel):
    """Structured character, scene, or visual style profile."""

    id: str | None = None
    asset_type: Literal["CHARACTER", "SCENE", "STYLE"]
    entity_key: str = Field(min_length=1, max_length=64)
    display_name: str = Field(min_length=1, max_length=120)
    profile_json: dict[str, Any] = Field(default_factory=dict)
    reference_image_url: str | None = None
    source: Literal["user_ground_truth", "agent_inferred"] = "agent_inferred"
    is_locked: bool = False


class ExtractedAssetsPayload(BaseModel):
    """Structured LLM output schema for Gate 1 asset extraction."""

    assets: list[AssetCard] = Field(min_length=1)


class FrameScript(BaseModel):
    """Structured storyboard frame script stored in story_frames.script_json."""

    id: str | None = None
    frame_index: int = Field(ge=1)
    shot_type: str
    camera_angle: str
    composition: str
    lighting: str
    action: str
    dialogue: str = ""
    entities: list[str] = Field(default_factory=list)


class EntityStateSnapshot(BaseModel):
    """Per-frame entity state persisted in episodic_entity_states."""

    frame_index: int = Field(ge=1)
    entity_key: str = Field(min_length=1, max_length=64)
    state_json: dict[str, Any] = Field(default_factory=dict)
    diff_from_prev: dict[str, Any] | None = None


class StoryboardPlanPayload(BaseModel):
    """Structured LLM output schema for Gate 2 storyboard breakdown."""

    frames: list[FrameScript] = Field(min_length=1)
    entity_states: list[EntityStateSnapshot] = Field(default_factory=list)


class PromptRewritePayload(BaseModel):
    """Structured LLM output for Gate 3 local prompt rewrite."""

    rewritten_prompt: str = Field(min_length=1)
    updated_entity_states: list[EntityStateSnapshot] = Field(default_factory=list)


class ParseStoryRequest(BaseModel):
    """Payload for POST /internal/v1/workflows/parse-story."""

    job_id: str
    project_id: str | None = None
    thread_id: str
    raw_story: str = Field(min_length=1)
    style_preset: str = "cinematic_melancholy"
    aspect_ratio: str = "16:9"
    target_frames: int = Field(default=4, ge=2, le=10)


class ParseStoryResponse(BaseModel):
    """Response for POST /internal/v1/workflows/parse-story."""

    job_id: str
    thread_id: str
    status: str
    current_stage: str
    assets: list[AssetCard]
    trace_id: str


class ResumeWorkflowRequest(BaseModel):
    """Payload for POST /internal/v1/workflows/resume."""

    thread_id: str
    job_id: str | None = None
    project_id: str | None = None
    gate_no: Literal[1, 2]
    user_edits: dict[str, Any] = Field(default_factory=dict)


class ResumeWorkflowResponse(BaseModel):
    """Response for POST /internal/v1/workflows/resume."""

    job_id: str | None = None
    thread_id: str
    status: str
    current_stage: str
    frames: list[FrameScript] = Field(default_factory=list)
    entity_states: list[EntityStateSnapshot] = Field(default_factory=list)
    trace_id: str


class RenderFrameRequest(BaseModel):
    """Payload for POST /internal/v1/frames/{frame_id}/render."""

    job_id: str | None = None
    project_id: str | None = None
    aspect_ratio: str | None = None
    seed: int | None = None


class RegenerateFrameRequest(BaseModel):
    """Payload for POST /internal/v1/frames/{frame_id}/regenerate."""

    job_id: str | None = None
    user_edit_note: str = Field(min_length=1)
    version_no: int | None = None
    cascade_downstream: bool = False
    seed: int | None = None


class FrameVersionResponse(BaseModel):
    """Response describing a created/active frame_versions record."""

    id: str | None = None
    frame_id: str
    version_no: int
    user_edit_note: str | None = None
    prompt_snapshot: str
    image_url: str
    seed: int | None = None
    vlm_score: float | None = None
    vlm_feedback: dict[str, Any] | None = None
    cascaded_frame_indices: list[int] = Field(default_factory=list)


class MemoryRetrieveRequest(BaseModel):
    """Payload for POST /internal/v1/memory/retrieve."""

    project_id: str
    job_id: str
    frame_index: int = Field(ge=1)
    query_text: str = ""
    frame_script: FrameScript | None = None
    style_preset: str = ""
    max_prompt_chars: int = Field(default=1200, ge=120, le=8000)
    top_k: int = Field(default=3, ge=1, le=20)


class MemoryRetrieveResponse(BaseModel):
    """Assembled and budget-trimmed generation context returned by WorkingContextBuilder."""

    assembled_prompt: str
    style_prefix: str
    locked_assets: list[AssetCard] = Field(default_factory=list)
    retrieved_assets: list[AssetCard] = Field(default_factory=list)
    prev_entity_states: list[EntityStateSnapshot] = Field(default_factory=list)
    reference_image_url: str | None = None
    dropped_conflicts: list[str] = Field(default_factory=list)
    estimated_tokens: int = 0
    trimmed: bool = False
