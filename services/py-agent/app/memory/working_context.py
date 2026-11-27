"""WorkingContextBuilder: layered memory recall, ground-truth override, and budget trimming."""

from __future__ import annotations

import logging
from typing import Any

from app.schemas import AssetCard, EntityStateSnapshot, FrameScript, MemoryRetrieveResponse

logger = logging.getLogger("echocanvas.working_context")


def merge_profile_dicts(
    base_profile: dict[str, Any],
    base_source: str,
    incoming_profile: dict[str, Any],
    incoming_source: str,
    entity_label: str = "profile",
) -> tuple[dict[str, Any], list[str]]:
    """Merge two attribute dictionaries; user_ground_truth strictly beats agent_inferred."""
    merged = dict(base_profile)
    dropped_conflicts: list[str] = []

    for key, incoming_val in incoming_profile.items():
        if key not in merged or merged[key] == incoming_val:
            merged[key] = incoming_val
            continue

        if base_source == "user_ground_truth" and incoming_source != "user_ground_truth":
            logger.warning(
                "dropping conflicting agent_inferred field %s.%s=%r; preserving user_ground_truth=%r",
                entity_label,
                key,
                incoming_val,
                merged[key],
            )
            dropped_conflicts.append(f"{entity_label}.{key}")
        elif incoming_source == "user_ground_truth" and base_source != "user_ground_truth":
            logger.info(
                "user_ground_truth field %s.%s=%r overrode agent_inferred=%r",
                entity_label,
                key,
                incoming_val,
                merged[key],
            )
            merged[key] = incoming_val
            dropped_conflicts.append(f"{entity_label}.{key}")
        else:
            merged[key] = incoming_val

    return merged, dropped_conflicts


def _format_kv(data: dict[str, Any]) -> str:
    return ", ".join(f"{k}={v}" for k, v in data.items() if v not in (None, "", [], {}))


class WorkingContextBuilder:
    """Assemble focused generation prompts respecting ground truth and context budget (TDD 5.3)."""

    def __init__(self, repo: Any) -> None:
        self.repo = repo

    def build(
        self,
        project_id: str,
        job_id: str,
        frame_index: int,
        frame_script: FrameScript | None = None,
        query_text: str = "",
        style_preset: str = "",
        max_prompt_chars: int = 1200,
        top_k: int = 3,
        inferred_overrides: list[AssetCard] | None = None,
    ) -> MemoryRetrieveResponse:
        dropped_conflicts: list[str] = []

        all_assets: list[AssetCard] = self.repo.list_project_assets(project_id)
        locked_assets: list[AssetCard] = self.repo.list_locked_assets(project_id)

        gt_by_key: dict[str, AssetCard] = {
            a.entity_key: a for a in all_assets if a.source == "user_ground_truth"
        }

        resolved_locked: list[AssetCard] = []
        for asset in locked_assets:
            gt = gt_by_key.get(asset.entity_key)
            if gt is not None and asset.source != "user_ground_truth":
                merged_prof, conflicts = merge_profile_dicts(
                    gt.profile_json,
                    "user_ground_truth",
                    asset.profile_json,
                    asset.source,
                    entity_label=asset.entity_key,
                )
                dropped_conflicts.extend(conflicts)
                asset = asset.model_copy(
                    update={"profile_json": merged_prof, "source": "user_ground_truth"}
                )
            resolved_locked.append(asset)

        search_query = query_text.strip()
        if not search_query and frame_script is not None:
            search_query = " ".join(
                part
                for part in (
                    frame_script.action,
                    frame_script.composition,
                    frame_script.lighting,
                    " ".join(frame_script.entities),
                )
                if part
            )

        locked_keys = {a.entity_key for a in resolved_locked}
        retrieved_raw: list[AssetCard] = self.repo.search_similar_assets(
            project_id,
            search_query,
            top_k=top_k,
            exclude_entity_keys=locked_keys,
        )

        # Resolve any inferred candidate overrides against user_ground_truth rules.
        inferred_by_key = {a.entity_key: a for a in (inferred_overrides or [])}
        for i, asset in enumerate(resolved_locked):
            if asset.entity_key in inferred_by_key:
                cand = inferred_by_key[asset.entity_key]
                merged_prof, conflicts = merge_profile_dicts(
                    asset.profile_json,
                    asset.source,
                    cand.profile_json,
                    cand.source,
                    entity_label=asset.entity_key,
                )
                dropped_conflicts.extend(conflicts)
                resolved_locked[i] = asset.model_copy(update={"profile_json": merged_prof})

        resolved_retrieved: list[AssetCard] = []
        for cand in retrieved_raw:
            gt = gt_by_key.get(cand.entity_key)
            if gt is not None and cand.source != "user_ground_truth":
                merged_prof, conflicts = merge_profile_dicts(
                    gt.profile_json,
                    "user_ground_truth",
                    cand.profile_json,
                    cand.source,
                    entity_label=cand.entity_key,
                )
                dropped_conflicts.extend(conflicts)
                resolved_retrieved.append(
                    cand.model_copy(update={"profile_json": merged_prof, "source": "user_ground_truth"})
                )
            else:
                resolved_retrieved.append(cand)

        prev_states_raw: list[EntityStateSnapshot] = (
            self.repo.get_episodic_states(job_id, frame_index - 1) if frame_index > 1 else []
        )
        prev_states: list[EntityStateSnapshot] = []
        for snap in prev_states_raw:
            gt = gt_by_key.get(snap.entity_key)
            if gt is not None:
                # Filter out state attributes that contradict immutable user_ground_truth traits.
                cleaned_state: dict[str, Any] = {}
                for k, v in snap.state_json.items():
                    if k in gt.profile_json and gt.profile_json[k] != v:
                        logger.warning(
                            "dropping episodic state %s.%s=%r conflicting with user_ground_truth=%r",
                            snap.entity_key,
                            k,
                            v,
                            gt.profile_json[k],
                        )
                        dropped_conflicts.append(f"{snap.entity_key}.{k}")
                    else:
                        cleaned_state[k] = v
                prev_states.append(snap.model_copy(update={"state_json": cleaned_state}))
            else:
                prev_states.append(snap)

        style_parts: list[str] = []
        if style_preset.strip():
            style_parts.append(f"preset={style_preset.strip()}")
        for asset in resolved_locked:
            if asset.asset_type == "STYLE":
                style_parts.append(f"{asset.display_name} ({_format_kv(asset.profile_json)})")
        style_prefix = f"[Style] {'; '.join(style_parts)}" if style_parts else ""

        char_scene_lines: list[str] = []
        for asset in resolved_locked:
            if asset.asset_type in ("CHARACTER", "SCENE"):
                kv = _format_kv(asset.profile_json)
                char_scene_lines.append(f"{asset.display_name}: {kv}" if kv else asset.display_name)
        traits_segment = f"[Locked Assets] {' | '.join(char_scene_lines)}" if char_scene_lines else ""

        delta_lines: list[str] = []
        for snap in prev_states:
            source_map = snap.diff_from_prev if snap.diff_from_prev else snap.state_json
            formatted = _format_kv(source_map)
            if formatted:
                delta_lines.append(f"{snap.entity_key}: {formatted}")
        prev_segment = f"[Prev State N-1] {' | '.join(delta_lines)}" if delta_lines else ""

        if frame_script is not None:
            action_segment = (
                f"[Frame {frame_index} Shot] {frame_script.shot_type}, {frame_script.camera_angle}; "
                f"composition: {frame_script.composition}; lighting: {frame_script.lighting}; "
                f"action: {frame_script.action}"
            )
        else:
            action_segment = f"[Frame {frame_index} Action] {search_query}" if search_query else ""

        bg_entries = [
            f"{a.display_name}: {_format_kv(a.profile_json)}"
            for a in resolved_retrieved
        ]

        # Enforce strict character budget: drop lowest-priority retrieved background hints first.
        trimmed = False
        kept_bg = list(bg_entries)
        core_segments = [s for s in (style_prefix, traits_segment, prev_segment, action_segment) if s]

        def _assemble(bgs: list[str], core: list[str]) -> str:
            parts = list(core)
            if bgs:
                parts.append(f"[Scene Memory] {' | '.join(bgs)}")
            return "\n".join(parts)

        assembled = _assemble(kept_bg, core_segments)
        while len(assembled) > max_prompt_chars and kept_bg:
            kept_bg.pop()
            trimmed = True
            assembled = _assemble(kept_bg, core_segments)

        # If still over budget with all retrieved background hints removed, drop prev_segment if needed, then slice.
        if len(assembled) > max_prompt_chars and prev_segment:
            trimmed = True
            trimmed_core = [s for s in (style_prefix, traits_segment, action_segment) if s]
            assembled = _assemble([], trimmed_core)
            if len(assembled) > max_prompt_chars:
                # Re-attempt keeping action + traits truncated cleanly.
                assembled = assembled[:max_prompt_chars]
        elif len(assembled) > max_prompt_chars:
            trimmed = True
            assembled = assembled[:max_prompt_chars]

        # Visual anchor rule: Frame 2..N require reference_image_url from locked CHARACTER asset or Frame 1.
        ref_url: str | None = None
        for asset in resolved_locked:
            if asset.asset_type == "CHARACTER" and asset.reference_image_url:
                ref_url = asset.reference_image_url
                break
        if ref_url is None and frame_index > 1:
            ref_url = self.repo.get_first_frame_reference_image(job_id)

        estimated_tokens = max(1, (len(assembled) + 3) // 4) if assembled else 0

        kept_count = len(kept_bg)
        return MemoryRetrieveResponse(
            assembled_prompt=assembled,
            style_prefix=style_prefix,
            locked_assets=resolved_locked,
            retrieved_assets=resolved_retrieved[:kept_count],
            prev_entity_states=prev_states,
            reference_image_url=ref_url,
            dropped_conflicts=dropped_conflicts,
            estimated_tokens=estimated_tokens,
            trimmed=trimmed,
        )
