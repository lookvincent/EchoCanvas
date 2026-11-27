"""LangGraph stateful story orchestration graph with HITL Gate 1 and Gate 2 interrupts."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from typing import Any, Callable, TypedDict

from langgraph.graph import END, START, StateGraph

from app.adapters.gateway_client import GatewayClient
from app.schemas import (
    AssetCard,
    EntityStateSnapshot,
    ExtractedAssetsPayload,
    FrameScript,
    StoryboardPlanPayload,
)


class StoryGraphState(TypedDict, total=False):
    """Typed graph state persisted across checkpoints by LangGraph Checkpointer."""

    job_id: str
    project_id: str
    raw_story: str
    style_preset: str
    aspect_ratio: str
    target_frames: int
    trace_id: str
    assets: list[dict[str, Any]]
    frames: list[dict[str, Any]]
    entity_states: list[dict[str, Any]]
    gate_status: str
    gate1_confirmed: bool
    gate2_confirmed: bool
    last_error: str | None


@dataclass
class StoryGraphCallbacks:
    """Optional persistence/notification hooks triggered as nodes complete."""

    on_assets_extracted: Callable[[StoryGraphState, list[AssetCard]], None] | None = None
    on_script_planned: Callable[
        [StoryGraphState, list[FrameScript], list[EntityStateSnapshot]], None
    ] | None = None
    on_dispatch_render: Callable[[StoryGraphState], None] | None = None


def _normalize_entity_key(raw: str, fallback: str) -> str:
    slug = re.sub(r"[^a-z0-9_]+", "_", raw.strip().lower()).strip("_")
    if not slug:
        slug = fallback
    return slug[:64]


def _normalize_assets(assets: list[AssetCard]) -> list[AssetCard]:
    seen: set[str] = set()
    normalized: list[AssetCard] = []
    for idx, asset in enumerate(assets, 1):
        base_key = _normalize_entity_key(asset.entity_key, f"asset_{idx}")
        key = base_key
        suffix = 2
        while key in seen:
            tail = f"_{suffix}"
            key = base_key[: 64 - len(tail)] + tail
            suffix += 1
        seen.add(key)
        normalized.append(asset.model_copy(update={"entity_key": key}))
    return normalized


def _normalize_storyboard(plan: StoryboardPlanPayload) -> tuple[list[FrameScript], list[EntityStateSnapshot]]:
    sorted_frames = sorted(plan.frames, key=lambda f: f.frame_index)
    frames = [f.model_copy(update={"frame_index": idx}) for idx, f in enumerate(sorted_frames, 1)]
    states = sorted(plan.entity_states, key=lambda s: (s.frame_index, s.entity_key))
    return frames, states


def build_story_graph(
    gateway_client: GatewayClient,
    checkpointer: Any,
    callbacks: StoryGraphCallbacks | None = None,
):
    """Compile the EchoCanvas LangGraph workflow with Gate 1 & Gate 2 HITL interrupts."""

    cb = callbacks or StoryGraphCallbacks()

    def extract_assets_node(state: StoryGraphState) -> dict[str, Any]:
        prompt = (
            "Extract recurring visual assets (CHARACTER, SCENE, STYLE) from the narrative below.\n"
            f"Style preset: {state.get('style_preset', 'cinematic_melancholy')}\n"
            f"Aspect ratio: {state.get('aspect_ratio', '16:9')}\n"
            "Return strict JSON matching schema ExtractedAssetsPayload: "
            '{"assets": [{"asset_type": "CHARACTER|SCENE|STYLE", "entity_key": "snake_case_id", '
            '"display_name": "...", "profile_json": {...}, "source": "agent_inferred", "is_locked": false}]}.\n\n'
            f"Story:\n{state['raw_story']}"
        )
        extracted = gateway_client.chat_json(
            messages=[
                {
                    "role": "system",
                    "content": "You are a visual narrative director extracting structured story bible assets as valid JSON.",
                },
                {"role": "user", "content": prompt},
            ],
            schema_cls=ExtractedAssetsPayload,
            job_id=state.get("job_id"),
            trace_id=state.get("trace_id", ""),
            call_type="LLM_TEXT",
        )
        assets = _normalize_assets(extracted.assets)
        if cb.on_assets_extracted is not None:
            cb.on_assets_extracted(state, assets)
        return {
            "assets": [a.model_dump(mode="json") for a in assets],
            "gate_status": "WAITING_GATE_1",
            "last_error": None,
        }

    def plan_script_node(state: StoryGraphState) -> dict[str, Any]:
        target_frames = int(state.get("target_frames") or 4)
        assets_json = json.dumps(state.get("assets") or [], ensure_ascii=False)
        prompt = (
            f"Break the story into exactly {target_frames} sequential storyboard frames and entity state snapshots.\n"
            f"Locked/Confirmed Assets JSON:\n{assets_json}\n\n"
            f"Aspect ratio: {state.get('aspect_ratio', '16:9')}, Style preset: {state.get('style_preset', 'cinematic_melancholy')}\n"
            "Strictly respect any fields marked source='user_ground_truth'.\n"
            "Return strict JSON matching schema StoryboardPlanPayload: "
            '{"frames": [{"frame_index": 1, "shot_type": "...", "camera_angle": "...", '
            '"composition": "...", "lighting": "...", "action": "...", "dialogue": "...", "entities": ["..."]}], '
            '"entity_states": [{"frame_index": 1, "entity_key": "...", "state_json": {...}}]}.\n\n'
            f"Story:\n{state['raw_story']}"
        )
        plan = gateway_client.chat_json(
            messages=[
                {
                    "role": "system",
                    "content": "You are a storyboard director designing continuous multi-frame scripts and entity state tracking as valid JSON.",
                },
                {"role": "user", "content": prompt},
            ],
            schema_cls=StoryboardPlanPayload,
            job_id=state.get("job_id"),
            trace_id=state.get("trace_id", ""),
            call_type="LLM_TEXT",
        )
        frames, entity_states = _normalize_storyboard(plan)
        if cb.on_script_planned is not None:
            cb.on_script_planned(state, frames, entity_states)
        return {
            "frames": [f.model_dump(mode="json") for f in frames],
            "entity_states": [s.model_dump(mode="json") for s in entity_states],
            "gate_status": "WAITING_GATE_2",
            "last_error": None,
        }

    def dispatch_render_node(state: StoryGraphState) -> dict[str, Any]:
        if cb.on_dispatch_render is not None:
            cb.on_dispatch_render(state)
        return {
            "gate_status": "READY_TO_RENDER",
            "last_error": None,
        }

    builder = StateGraph(StoryGraphState)
    builder.add_node("extract_assets_node", extract_assets_node)
    builder.add_node("plan_script_node", plan_script_node)
    builder.add_node("dispatch_render_node", dispatch_render_node)

    builder.add_edge(START, "extract_assets_node")
    builder.add_edge("extract_assets_node", "plan_script_node")
    builder.add_edge("plan_script_node", "dispatch_render_node")
    builder.add_edge("dispatch_render_node", END)

    return builder.compile(
        checkpointer=checkpointer,
        interrupt_before=["plan_script_node", "dispatch_render_node"],
    )
