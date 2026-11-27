"""PostgreSQL and in-memory repositories for asset_style_profiles and episodic_entity_states."""

from __future__ import annotations

import json
import math
from typing import Any

from app.memory.embeddings import compute_deterministic_embedding, format_pgvector
from app.schemas import AssetCard, EntityStateSnapshot


def compute_state_diff(
    prev_state: dict[str, Any] | None,
    curr_state: dict[str, Any],
) -> dict[str, Any] | None:
    """Compute per-field delta from previous frame entity state."""
    if prev_state is None:
        return None
    diff: dict[str, Any] = {}
    all_keys = sorted(set(prev_state.keys()) | set(curr_state.keys()))
    for key in all_keys:
        old_val = prev_state.get(key)
        new_val = curr_state.get(key)
        if old_val != new_val:
            diff[key] = {"from": old_val, "to": new_val}
    return diff


def _asset_embed_text(asset: AssetCard) -> str:
    profile_str = json.dumps(asset.profile_json, ensure_ascii=False, sort_keys=True)
    return f"{asset.asset_type} {asset.entity_key} {asset.display_name} {profile_str}"


def _cosine_similarity(a: list[float], b: list[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    if na == 0.0 or nb == 0.0:
        return 0.0
    return dot / (na * nb)


class MemoryRepository:
    """PostgreSQL + pgvector repository for long-term assets and episodic entity states."""

    def __init__(self, pool: Any) -> None:
        self.pool = pool

    def upsert_assets(
        self,
        project_id: str,
        assets: list[AssetCard],
        force_user_ground_truth: bool = False,
    ) -> list[AssetCard]:
        """Upsert asset profiles with embedding; agent_inferred never overwrites user_ground_truth."""
        saved: list[AssetCard] = []
        with self.pool.connection() as conn:
            with conn.transaction():
                for raw_asset in assets:
                    asset = raw_asset
                    if force_user_ground_truth:
                        asset = raw_asset.model_copy(
                            update={"source": "user_ground_truth", "is_locked": True}
                        )
                    vec_lit = format_pgvector(
                        compute_deterministic_embedding(_asset_embed_text(asset))
                    )
                    row = conn.execute(
                        """
                        INSERT INTO asset_style_profiles (
                            project_id, asset_type, entity_key, display_name,
                            profile_json, reference_image_url, source, is_locked, embedding
                        )
                        VALUES (%s, %s, %s, %s, %s::jsonb, %s, %s, %s, %s::vector)
                        ON CONFLICT (project_id, entity_key) DO UPDATE SET
                            asset_type = EXCLUDED.asset_type,
                            display_name = EXCLUDED.display_name,
                            profile_json = EXCLUDED.profile_json,
                            reference_image_url = COALESCE(
                                EXCLUDED.reference_image_url,
                                asset_style_profiles.reference_image_url
                            ),
                            source = EXCLUDED.source,
                            is_locked = EXCLUDED.is_locked,
                            embedding = EXCLUDED.embedding
                        WHERE asset_style_profiles.source <> 'user_ground_truth'
                           OR EXCLUDED.source = 'user_ground_truth'
                        RETURNING id::text AS id, asset_type, entity_key, display_name,
                                  profile_json, reference_image_url, source, is_locked
                        """,
                        (
                            project_id,
                            asset.asset_type,
                            asset.entity_key,
                            asset.display_name,
                            json.dumps(asset.profile_json, ensure_ascii=False),
                            asset.reference_image_url,
                            asset.source,
                            asset.is_locked,
                            vec_lit,
                        ),
                    ).fetchone()
                    if row is None:
                        row = conn.execute(
                            """
                            SELECT id::text AS id, asset_type, entity_key, display_name,
                                   profile_json, reference_image_url, source, is_locked
                            FROM asset_style_profiles
                            WHERE project_id = %s AND entity_key = %s
                            """,
                            (project_id, asset.entity_key),
                        ).fetchone()
                    saved.append(AssetCard.model_validate(row))
        return saved

    def list_project_assets(self, project_id: str) -> list[AssetCard]:
        """List all assets for a project ordered by creation timestamp."""
        with self.pool.connection() as conn:
            rows = conn.execute(
                """
                SELECT id::text AS id, asset_type, entity_key, display_name,
                       profile_json, reference_image_url, source, is_locked
                FROM asset_style_profiles
                WHERE project_id = %s
                ORDER BY created_at ASC, entity_key ASC
                """,
                (project_id,),
            ).fetchall()
        return [AssetCard.model_validate(r) for r in rows]

    def list_locked_assets(self, project_id: str) -> list[AssetCard]:
        """List locked core assets (is_locked = TRUE) for global recall."""
        with self.pool.connection() as conn:
            rows = conn.execute(
                """
                SELECT id::text AS id, asset_type, entity_key, display_name,
                       profile_json, reference_image_url, source, is_locked
                FROM asset_style_profiles
                WHERE project_id = %s AND is_locked = TRUE
                ORDER BY created_at ASC, entity_key ASC
                """,
                (project_id,),
            ).fetchall()
        return [AssetCard.model_validate(r) for r in rows]

    def search_similar_assets(
        self,
        project_id: str,
        query_text: str,
        top_k: int = 3,
        exclude_entity_keys: set[str] | None = None,
    ) -> list[AssetCard]:
        """Run pgvector cosine Top-K similarity lookup over asset_style_profiles."""
        if not query_text.strip() or top_k <= 0:
            return []

        query_vec = format_pgvector(compute_deterministic_embedding(query_text))
        excluded = sorted(exclude_entity_keys) if exclude_entity_keys else []

        with self.pool.connection() as conn:
            if excluded:
                rows = conn.execute(
                    """
                    SELECT id::text AS id, asset_type, entity_key, display_name,
                           profile_json, reference_image_url, source, is_locked
                    FROM asset_style_profiles
                    WHERE project_id = %s
                      AND embedding IS NOT NULL
                      AND NOT (entity_key = ANY(%s))
                    ORDER BY embedding <=> %s::vector ASC
                    LIMIT %s
                    """,
                    (project_id, excluded, query_vec, top_k),
                ).fetchall()
            else:
                rows = conn.execute(
                    """
                    SELECT id::text AS id, asset_type, entity_key, display_name,
                           profile_json, reference_image_url, source, is_locked
                    FROM asset_style_profiles
                    WHERE project_id = %s
                      AND embedding IS NOT NULL
                    ORDER BY embedding <=> %s::vector ASC
                    LIMIT %s
                    """,
                    (project_id, query_vec, top_k),
                ).fetchall()
        return [AssetCard.model_validate(r) for r in rows]

    def get_episodic_states(self, job_id: str, frame_index: int) -> list[EntityStateSnapshot]:
        """Load episodic entity state snapshots for (job_id, frame_index)."""
        if frame_index < 1:
            return []
        with self.pool.connection() as conn:
            rows = conn.execute(
                """
                SELECT frame_index, entity_key, state_json, diff_from_prev
                FROM episodic_entity_states
                WHERE job_id = %s AND frame_index = %s
                ORDER BY entity_key ASC
                """,
                (job_id, frame_index),
            ).fetchall()
        return [EntityStateSnapshot.model_validate(r) for r in rows]

    def upsert_episodic_states(
        self,
        job_id: str,
        frame_index: int,
        states: list[EntityStateSnapshot],
    ) -> list[EntityStateSnapshot]:
        """Upsert entity states for a frame and compute diff_from_prev from N-1 automatically."""
        if not states:
            return []

        prev_by_key: dict[str, dict[str, Any]] = {}
        if frame_index > 1:
            for prev in self.get_episodic_states(job_id, frame_index - 1):
                prev_by_key[prev.entity_key] = prev.state_json

        saved: list[EntityStateSnapshot] = []
        with self.pool.connection() as conn:
            with conn.transaction():
                for snap in states:
                    diff = snap.diff_from_prev
                    if diff is None and frame_index > 1:
                        diff = compute_state_diff(
                            prev_by_key.get(snap.entity_key), snap.state_json
                        )
                    row = conn.execute(
                        """
                        INSERT INTO episodic_entity_states (
                            job_id, frame_index, entity_key, state_json, diff_from_prev
                        )
                        VALUES (%s, %s, %s, %s::jsonb, %s::jsonb)
                        ON CONFLICT (job_id, frame_index, entity_key) DO UPDATE SET
                            state_json = EXCLUDED.state_json,
                            diff_from_prev = EXCLUDED.diff_from_prev
                        RETURNING frame_index, entity_key, state_json, diff_from_prev
                        """,
                        (
                            job_id,
                            frame_index,
                            snap.entity_key,
                            json.dumps(snap.state_json, ensure_ascii=False),
                            json.dumps(diff, ensure_ascii=False) if diff is not None else None,
                        ),
                    ).fetchone()
                    saved.append(EntityStateSnapshot.model_validate(row))
        return saved

    def get_first_frame_reference_image(self, job_id: str) -> str | None:
        """Fetch active image_url of Frame 1 for cross-frame IP-Adapter visual anchoring."""
        with self.pool.connection() as conn:
            row = conn.execute(
                """
                SELECT fv.image_url
                FROM story_frames sf
                INNER JOIN frame_versions fv ON fv.id = sf.active_version_id
                WHERE sf.job_id = %s AND sf.frame_index = 1
                LIMIT 1
                """,
                (job_id,),
            ).fetchone()
        if not row:
            return None
        return row["image_url"]


class InMemoryMemoryRepository:
    """In-memory double of MemoryRepository for fast deterministic unit tests."""

    def __init__(self) -> None:
        self._assets: dict[str, dict[str, tuple[AssetCard, list[float]]]] = {}
        self._states: dict[tuple[str, int], dict[str, EntityStateSnapshot]] = {}
        self._first_frame_urls: dict[str, str] = {}

    def set_first_frame_reference_image(self, job_id: str, image_url: str) -> None:
        self._first_frame_urls[job_id] = image_url

    def get_first_frame_reference_image(self, job_id: str) -> str | None:
        return self._first_frame_urls.get(job_id)

    def upsert_assets(
        self,
        project_id: str,
        assets: list[AssetCard],
        force_user_ground_truth: bool = False,
    ) -> list[AssetCard]:
        bucket = self._assets.setdefault(project_id, {})
        result: list[AssetCard] = []
        for raw_asset in assets:
            asset = raw_asset
            if force_user_ground_truth:
                asset = raw_asset.model_copy(
                    update={"source": "user_ground_truth", "is_locked": True}
                )
            existing = bucket.get(asset.entity_key)
            if existing is not None:
                prev_asset, _ = existing
                if prev_asset.source == "user_ground_truth" and asset.source != "user_ground_truth":
                    result.append(prev_asset)
                    continue
                if asset.reference_image_url is None:
                    asset = asset.model_copy(
                        update={"reference_image_url": prev_asset.reference_image_url}
                    )
            if asset.id is None:
                asset = asset.model_copy(update={"id": f"mem_{project_id}_{asset.entity_key}"})
            vec = compute_deterministic_embedding(_asset_embed_text(asset))
            bucket[asset.entity_key] = (asset, vec)
            result.append(asset)
        return result

    def list_project_assets(self, project_id: str) -> list[AssetCard]:
        return [item[0] for item in self._assets.get(project_id, {}).values()]

    def list_locked_assets(self, project_id: str) -> list[AssetCard]:
        return [
            asset for asset in self.list_project_assets(project_id) if asset.is_locked
        ]

    def search_similar_assets(
        self,
        project_id: str,
        query_text: str,
        top_k: int = 3,
        exclude_entity_keys: set[str] | None = None,
    ) -> list[AssetCard]:
        if not query_text.strip() or top_k <= 0:
            return []
        excluded = exclude_entity_keys or set()
        query_vec = compute_deterministic_embedding(query_text)
        scored: list[tuple[float, AssetCard]] = []
        for entity_key, (asset, vec) in self._assets.get(project_id, {}).items():
            if entity_key in excluded:
                continue
            scored.append((_cosine_similarity(query_vec, vec), asset))
        scored.sort(key=lambda pair: pair[0], reverse=True)
        return [asset for _, asset in scored[:top_k]]

    def get_episodic_states(self, job_id: str, frame_index: int) -> list[EntityStateSnapshot]:
        if frame_index < 1:
            return []
        entries = self._states.get((job_id, frame_index), {})
        return [entries[k] for k in sorted(entries.keys())]

    def upsert_episodic_states(
        self,
        job_id: str,
        frame_index: int,
        states: list[EntityStateSnapshot],
    ) -> list[EntityStateSnapshot]:
        prev_map = (
            {s.entity_key: s.state_json for s in self.get_episodic_states(job_id, frame_index - 1)}
            if frame_index > 1
            else {}
        )
        bucket = self._states.setdefault((job_id, frame_index), {})
        saved: list[EntityStateSnapshot] = []
        for snap in states:
            diff = snap.diff_from_prev
            if diff is None and frame_index > 1:
                diff = compute_state_diff(prev_map.get(snap.entity_key), snap.state_json)
            stored = snap.model_copy(update={"frame_index": frame_index, "diff_from_prev": diff})
            bucket[snap.entity_key] = stored
            saved.append(stored)
        return saved
