"""WorkflowService: coordinates LangGraph checkpoints, memory recall, rendering, QA, and regen."""

from __future__ import annotations

import json
from collections import defaultdict
from contextlib import nullcontext
from typing import Any

from app.adapters.gateway_client import GatewayClient
from app.adapters.vision_adapter import VisionAdapter
from app.memory.working_context import WorkingContextBuilder
from app.observability.tracing import TraceRecorder
from app.quality.vlm_checker import VLMQualityChecker
from app.repository.story_repo import InMemoryStoryRepository
from app.schemas import (
    AssetCard,
    EntityStateSnapshot,
    FrameScript,
    FrameVersionResponse,
    ParseStoryRequest,
    ParseStoryResponse,
    PromptRewritePayload,
    RegenerateFrameRequest,
    RenderFrameRequest,
    ResumeWorkflowRequest,
    ResumeWorkflowResponse,
)


class WorkflowConflictError(ValueError):
    """Raised when a workflow action is invalid for the thread's current gate state."""


class WorkflowService:
    """High-level coordinator bridging LangGraph state, DB persistence, VLM QA, and Go Gateway."""

    def __init__(
        self,
        graph: Any,
        gateway_client: GatewayClient,
        memory_repo: Any,
        story_repo: Any,
        context_builder: WorkingContextBuilder,
        vision_adapter: VisionAdapter | None = None,
        vlm_checker: VLMQualityChecker | None = None,
        trace_recorder: TraceRecorder | None = None,
    ) -> None:
        self.graph = graph
        self.gateway_client = gateway_client
        self.memory_repo = memory_repo
        self.story_repo = story_repo
        self.context_builder = context_builder
        self.vision_adapter = vision_adapter or VisionAdapter(gateway_client)
        self.trace_recorder = trace_recorder
        self.vlm_checker = vlm_checker or VLMQualityChecker(
            gateway_client, trace_recorder=trace_recorder
        )

    def _span(
        self,
        name: str,
        trace_id: str,
        job_id: str = "",
        frame_id: str = "",
    ):
        if self.trace_recorder is None:
            return nullcontext({})
        return self.trace_recorder.span(
            span_name=name,
            trace_id=trace_id,
            job_id=job_id,
            frame_id=frame_id,
        )

    def parse_story(self, payload: ParseStoryRequest, trace_id: str) -> ParseStoryResponse:
        with self._span("workflow.parse_story", trace_id=trace_id, job_id=payload.job_id):
            if isinstance(self.story_repo, InMemoryStoryRepository):
                self.story_repo.ensure_job(
                    job_id=payload.job_id,
                    project_id=payload.project_id or "",
                    thread_id=payload.thread_id,
                    aspect_ratio=payload.aspect_ratio,
                    style_preset=payload.style_preset,
                )

            config = {"configurable": {"thread_id": payload.thread_id}}
            initial_state = {
                "job_id": payload.job_id,
                "project_id": payload.project_id or "",
                "raw_story": payload.raw_story,
                "style_preset": payload.style_preset,
                "aspect_ratio": payload.aspect_ratio,
                "target_frames": payload.target_frames,
                "trace_id": trace_id,
            }
            result = self.graph.invoke(initial_state, config=config)

            extracted = [AssetCard.model_validate(item) for item in (result.get("assets") or [])]
            if payload.project_id:
                saved_assets = self.memory_repo.upsert_assets(payload.project_id, extracted)
            else:
                saved_assets = extracted

            self.story_repo.update_job_status(payload.job_id, "WAITING_GATE", "GATE_1")

            return ParseStoryResponse(
                job_id=payload.job_id,
                thread_id=payload.thread_id,
                status="WAITING_GATE",
                current_stage="GATE_1",
                assets=saved_assets,
                trace_id=trace_id,
            )

    def _apply_gate1_asset_edits(
        self,
        project_id: str,
        existing_assets: list[AssetCard],
        asset_edits: list[dict[str, Any]],
    ) -> list[AssetCard]:
        existing_by_key = {a.entity_key: a for a in existing_assets}
        existing_by_id = {a.id: a for a in existing_assets if a.id}

        to_upsert: list[AssetCard] = []
        for raw in asset_edits:
            base = None
            if raw.get("id") and raw["id"] in existing_by_id:
                base = existing_by_id[raw["id"]]
            elif raw.get("entity_key") and raw["entity_key"] in existing_by_key:
                base = existing_by_key[raw["entity_key"]]

            if base is not None:
                merged_profile = dict(base.profile_json)
                if isinstance(raw.get("profile_json"), dict):
                    merged_profile.update(raw["profile_json"])
                card = base.model_copy(
                    update={
                        "display_name": raw.get("display_name", base.display_name),
                        "asset_type": raw.get("asset_type", base.asset_type),
                        "profile_json": merged_profile,
                        "reference_image_url": raw.get("reference_image_url", base.reference_image_url),
                        "source": "user_ground_truth",
                        "is_locked": bool(raw.get("is_locked", True)),
                    }
                )
            else:
                card = AssetCard.model_validate(
                    {
                        **raw,
                        "source": "user_ground_truth",
                        "is_locked": bool(raw.get("is_locked", True)),
                    }
                )
            to_upsert.append(card)

        if project_id and to_upsert:
            self.memory_repo.upsert_assets(project_id, to_upsert, force_user_ground_truth=True)
            return self.memory_repo.list_project_assets(project_id)

        merged_map = {a.entity_key: a for a in existing_assets}
        for c in to_upsert:
            merged_map[c.entity_key] = c
        return list(merged_map.values())

    def resume_workflow(
        self,
        payload: ResumeWorkflowRequest,
        trace_id: str,
    ) -> ResumeWorkflowResponse:
        config = {"configurable": {"thread_id": payload.thread_id}}
        snapshot = self.graph.get_state(config)
        if not snapshot or not snapshot.values:
            raise LookupError(f"workflow checkpoint not found for thread_id={payload.thread_id}")

        state_vals = dict(snapshot.values)
        next_nodes = tuple(snapshot.next or ())
        job_id = payload.job_id or state_vals.get("job_id") or ""
        project_id = payload.project_id or state_vals.get("project_id") or ""

        with self._span("workflow.resume", trace_id=trace_id, job_id=job_id):
            if payload.gate_no == 1:
                if "plan_script_node" not in next_nodes:
                    raise WorkflowConflictError(
                        f"thread {payload.thread_id} is not waiting at Gate 1 (next={next_nodes})"
                    )

                current_assets = (
                    self.memory_repo.list_project_assets(project_id)
                    if project_id
                    else [AssetCard.model_validate(a) for a in (state_vals.get("assets") or [])]
                )
                asset_edits = payload.user_edits.get("assets")
                if isinstance(asset_edits, list) and asset_edits:
                    current_assets = self._apply_gate1_asset_edits(
                        project_id, current_assets, asset_edits
                    )

                self.graph.update_state(
                    config,
                    {
                        "assets": [a.model_dump(mode="json") for a in current_assets],
                        "gate1_confirmed": True,
                        "trace_id": trace_id,
                    },
                )
                result = self.graph.invoke(None, config=config)

                raw_frames = [FrameScript.model_validate(f) for f in (result.get("frames") or [])]
                saved_frames = (
                    self.story_repo.upsert_story_frames(job_id, raw_frames)
                    if job_id
                    else raw_frames
                )

                raw_states = [
                    EntityStateSnapshot.model_validate(s) for s in (result.get("entity_states") or [])
                ]
                grouped: dict[int, list[EntityStateSnapshot]] = defaultdict(list)
                for st in raw_states:
                    grouped[st.frame_index].append(st)

                persisted_states: list[EntityStateSnapshot] = []
                if job_id:
                    for fidx in sorted(grouped.keys()):
                        persisted_states.extend(
                            self.memory_repo.upsert_episodic_states(job_id, fidx, grouped[fidx])
                        )
                    self.story_repo.update_job_status(job_id, "WAITING_GATE", "GATE_2")
                else:
                    persisted_states = raw_states

                return ResumeWorkflowResponse(
                    job_id=job_id or None,
                    thread_id=payload.thread_id,
                    status="WAITING_GATE",
                    current_stage="GATE_2",
                    frames=saved_frames,
                    entity_states=persisted_states,
                    trace_id=trace_id,
                )

            # gate_no == 2
            if "dispatch_render_node" not in next_nodes:
                raise WorkflowConflictError(
                    f"thread {payload.thread_id} is not waiting at Gate 2 (next={next_nodes})"
                )

            frame_edits = payload.user_edits.get("frames")
            if job_id and isinstance(frame_edits, list) and frame_edits:
                saved_frames = self.story_repo.apply_frame_edits(job_id, frame_edits)
            elif job_id:
                rows = self.story_repo.list_frames(job_id)
                saved_frames = []
                for r in rows:
                    script_data = dict(r["script_json"] or {})
                    script_data["frame_index"] = r["frame_index"]
                    script_data["id"] = r["id"]
                    saved_frames.append(FrameScript.model_validate(script_data))
            else:
                saved_frames = [FrameScript.model_validate(f) for f in (state_vals.get("frames") or [])]

            self.graph.update_state(
                config,
                {
                    "frames": [f.model_dump(mode="json") for f in saved_frames],
                    "gate2_confirmed": True,
                    "trace_id": trace_id,
                },
            )
            self.graph.invoke(None, config=config)

            if job_id:
                self.story_repo.update_job_status(job_id, "RUNNING", "RENDER_FRAMES")

            return ResumeWorkflowResponse(
                job_id=job_id or None,
                thread_id=payload.thread_id,
                status="RUNNING",
                current_stage="RENDER_FRAMES",
                frames=saved_frames,
                entity_states=[
                    EntityStateSnapshot.model_validate(s)
                    for s in (state_vals.get("entity_states") or [])
                ],
                trace_id=trace_id,
            )

    def render_frame(
        self,
        frame_id: str,
        payload: RenderFrameRequest,
        trace_id: str,
    ) -> FrameVersionResponse:
        frame = self.story_repo.get_frame(frame_id)
        if frame is None:
            raise LookupError(f"story frame not found: {frame_id}")

        job_id = payload.job_id or frame["job_id"]
        project_id = payload.project_id or frame["project_id"]
        frame_index = int(frame["frame_index"])
        aspect_ratio = payload.aspect_ratio or frame.get("aspect_ratio") or "16:9"
        style_preset = frame.get("style_preset") or "cinematic_melancholy"

        script_data = dict(frame["script_json"] or {})
        script_data["frame_index"] = frame_index
        script_data["id"] = frame_id
        frame_script = FrameScript.model_validate(script_data)

        with self._span("workflow.render_frame", trace_id=trace_id, job_id=job_id, frame_id=frame_id):
            ctx = self.context_builder.build(
                project_id=project_id,
                job_id=job_id,
                frame_index=frame_index,
                frame_script=frame_script,
                style_preset=style_preset,
                max_prompt_chars=1200,
                top_k=3,
            )

            outcome = self.vlm_checker.render_and_verify(
                render_fn=lambda prompt, attempt: self.vision_adapter.render_frame_image(
                    prompt=prompt,
                    job_id=job_id,
                    frame_id=frame_id,
                    trace_id=trace_id,
                    project_id=project_id,
                    version_hint=1,
                    aspect_ratio=aspect_ratio,
                    reference_image_url=ctx.reference_image_url,
                    seed=payload.seed,
                ),
                base_prompt=ctx.assembled_prompt,
                frame_script=frame_script,
                locked_assets=ctx.locked_assets,
                prev_entity_states=ctx.prev_entity_states,
                job_id=job_id,
                frame_id=frame_id,
                trace_id=trace_id,
                reference_image_url=ctx.reference_image_url,
            )

            ver = self.story_repo.create_frame_version(
                frame_id=frame_id,
                prompt_snapshot=outcome.prompt_snapshot,
                image_url=outcome.image_url,
                seed=outcome.seed,
                vlm_score=outcome.vlm_result.score,
                vlm_feedback=outcome.vlm_result.model_dump(mode="json"),
            )
            if frame_index == 1:
                self.story_repo.set_character_reference_if_empty(project_id, outcome.image_url)

            # Seed current-frame episodic baseline if missing so Frame N+1 can diff cleanly.
            if not self.memory_repo.get_episodic_states(job_id, frame_index):
                entities = frame_script.entities or [
                    a.entity_key for a in ctx.locked_assets if a.asset_type == "CHARACTER"
                ]
                baseline_states = [
                    EntityStateSnapshot(
                        frame_index=frame_index,
                        entity_key=ekey,
                        state_json={"action": frame_script.action, "lighting": frame_script.lighting},
                    )
                    for ekey in entities
                ]
                if baseline_states:
                    self.memory_repo.upsert_episodic_states(job_id, frame_index, baseline_states)

            return ver

    def _cascade_downstream_states(
        self,
        job_id: str,
        current_frame_index: int,
        user_edit_note: str,
        updated_states: list[EntityStateSnapshot],
    ) -> list[int]:
        all_frames = self.story_repo.list_frames(job_id)
        downstream = [f for f in all_frames if int(f["frame_index"]) > current_frame_index]
        if not downstream:
            return []

        patch_by_entity = {st.entity_key: st.state_json for st in updated_states}
        if not patch_by_entity:
            for st in self.memory_repo.get_episodic_states(job_id, current_frame_index):
                patch_by_entity[st.entity_key] = st.state_json

        cascaded_indices: list[int] = []
        for df in sorted(downstream, key=lambda item: int(item["frame_index"])):
            df_idx = int(df["frame_index"])
            existing = self.memory_repo.get_episodic_states(job_id, df_idx)
            by_key = {st.entity_key: st for st in existing}

            new_snapshots: list[EntityStateSnapshot] = []
            for ekey in sorted(set(by_key.keys()) | set(patch_by_entity.keys())):
                base_state = dict(by_key[ekey].state_json) if ekey in by_key else {}
                for pk, pv in patch_by_entity.get(ekey, {}).items():
                    base_state.setdefault(pk, pv)
                    if pk in ("clothing_state", "held_items", "weather_lighting", "location"):
                        base_state[pk] = pv
                base_state["inherited_regen_note"] = user_edit_note
                new_snapshots.append(
                    EntityStateSnapshot(
                        frame_index=df_idx,
                        entity_key=ekey,
                        state_json=base_state,
                    )
                )

            if new_snapshots:
                self.memory_repo.upsert_episodic_states(job_id, df_idx, new_snapshots)
                cascaded_indices.append(df_idx)
        return cascaded_indices

    def regenerate_frame(
        self,
        frame_id: str,
        payload: RegenerateFrameRequest,
        trace_id: str,
    ) -> FrameVersionResponse:
        frame = self.story_repo.get_frame(frame_id)
        if frame is None:
            raise LookupError(f"story frame not found: {frame_id}")

        job_id = payload.job_id or frame["job_id"]
        project_id = frame["project_id"]
        frame_index = int(frame["frame_index"])
        aspect_ratio = frame.get("aspect_ratio") or "16:9"
        style_preset = frame.get("style_preset") or "cinematic_melancholy"

        script_data = dict(frame["script_json"] or {})
        script_data["frame_index"] = frame_index
        script_data["id"] = frame_id
        frame_script = FrameScript.model_validate(script_data)

        with self._span("workflow.regenerate_frame", trace_id=trace_id, job_id=job_id, frame_id=frame_id):
            ctx = self.context_builder.build(
                project_id=project_id,
                job_id=job_id,
                frame_index=frame_index,
                frame_script=frame_script,
                style_preset=style_preset,
                max_prompt_chars=1200,
                top_k=3,
            )

            locked_summary = json.dumps(
                [a.model_dump(mode="json") for a in ctx.locked_assets],
                ensure_ascii=False,
            )
            rewrite = self.gateway_client.chat_json(
                messages=[
                    {
                        "role": "system",
                        "content": (
                            "You are an image prompt editor. Rewrite the storyboard prompt to incorporate the user's "
                            "local edit note while strictly preserving every locked user_ground_truth attribute. "
                            'Return JSON: {"rewritten_prompt": "...", "updated_entity_states": [{"frame_index": N, "entity_key": "...", "state_json": {...}}]}.'
                        ),
                    },
                    {
                        "role": "user",
                        "content": (
                            f"Frame index: {frame_index}\n"
                            f"Locked Assets (IMMUTABLE): {locked_summary}\n"
                            f"Base Working Context Prompt:\n{ctx.assembled_prompt}\n\n"
                            f"User Local Edit Instruction (Gate 3): {payload.user_edit_note}"
                        ),
                    },
                ],
                schema_cls=PromptRewritePayload,
                job_id=job_id,
                frame_id=frame_id,
                trace_id=trace_id,
                call_type="LLM_TEXT",
            )

            outcome = self.vlm_checker.render_and_verify(
                render_fn=lambda prompt, attempt: self.vision_adapter.render_frame_image(
                    prompt=prompt,
                    job_id=job_id,
                    frame_id=frame_id,
                    trace_id=trace_id,
                    project_id=project_id,
                    version_hint=payload.version_no or 2,
                    aspect_ratio=aspect_ratio,
                    reference_image_url=ctx.reference_image_url,
                    seed=payload.seed,
                ),
                base_prompt=rewrite.rewritten_prompt,
                frame_script=frame_script,
                locked_assets=ctx.locked_assets,
                prev_entity_states=ctx.prev_entity_states,
                job_id=job_id,
                frame_id=frame_id,
                trace_id=trace_id,
                reference_image_url=ctx.reference_image_url,
            )

            ver = self.story_repo.create_frame_version(
                frame_id=frame_id,
                prompt_snapshot=outcome.prompt_snapshot,
                image_url=outcome.image_url,
                user_edit_note=payload.user_edit_note,
                seed=outcome.seed,
                vlm_score=outcome.vlm_result.score,
                vlm_feedback=outcome.vlm_result.model_dump(mode="json"),
            )

            normalized_states = [
                st.model_copy(update={"frame_index": frame_index})
                for st in rewrite.updated_entity_states
            ]
            if normalized_states:
                self.memory_repo.upsert_episodic_states(job_id, frame_index, normalized_states)

            cascaded: list[int] = []
            if payload.cascade_downstream:
                cascaded = self._cascade_downstream_states(
                    job_id=job_id,
                    current_frame_index=frame_index,
                    user_edit_note=payload.user_edit_note,
                    updated_states=normalized_states,
                )

            return ver.model_copy(update={"cascaded_frame_indices": cascaded})
