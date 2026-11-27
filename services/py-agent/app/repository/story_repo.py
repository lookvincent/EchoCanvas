"""PostgreSQL and in-memory repositories for story_jobs, story_frames, and frame_versions."""

from __future__ import annotations

import json
import uuid
from typing import Any

from app.schemas import FrameScript, FrameVersionResponse


class StoryRepository:
    """PostgreSQL repository for storyboard jobs, frames, and versions."""

    def __init__(self, pool: Any) -> None:
        self.pool = pool

    def get_job(self, job_id: str) -> dict[str, Any] | None:
        with self.pool.connection() as conn:
            return conn.execute(
                """
                SELECT j.id::text AS id, j.project_id::text AS project_id,
                       j.langgraph_thread_id, j.trace_id, j.status, j.current_stage,
                       p.aspect_ratio, p.style_preset, p.target_frames
                FROM story_jobs j
                INNER JOIN projects p ON p.id = j.project_id
                WHERE j.id = %s
                """,
                (job_id,),
            ).fetchone()

    def update_job_status(
        self,
        job_id: str,
        status: str,
        current_stage: str,
        error_message: str | None = None,
    ) -> None:
        with self.pool.connection() as conn:
            conn.execute(
                """
                UPDATE story_jobs
                SET status = %s,
                    current_stage = %s,
                    error_message = COALESCE(%s, error_message)
                WHERE id = %s
                """,
                (status, current_stage, error_message, job_id),
            )

    def upsert_story_frames(self, job_id: str, frames: list[FrameScript]) -> list[FrameScript]:
        saved: list[FrameScript] = []
        with self.pool.connection() as conn:
            with conn.transaction():
                for frame in frames:
                    script_dict = frame.model_dump(exclude={"id"}, mode="json")
                    row = conn.execute(
                        """
                        INSERT INTO story_frames (job_id, frame_index, script_json)
                        VALUES (%s, %s, %s::jsonb)
                        ON CONFLICT (job_id, frame_index) DO UPDATE SET
                            script_json = EXCLUDED.script_json
                        RETURNING id::text AS id, frame_index, script_json
                        """,
                        (
                            job_id,
                            frame.frame_index,
                            json.dumps(script_dict, ensure_ascii=False),
                        ),
                    ).fetchone()
                    payload = dict(row["script_json"])
                    payload["frame_index"] = row["frame_index"]
                    payload["id"] = row["id"]
                    saved.append(FrameScript.model_validate(payload))
        return saved

    def list_frames(self, job_id: str) -> list[dict[str, Any]]:
        with self.pool.connection() as conn:
            rows = conn.execute(
                """
                SELECT id::text AS id, job_id::text AS job_id, frame_index,
                       script_json, active_version_id::text AS active_version_id
                FROM story_frames
                WHERE job_id = %s
                ORDER BY frame_index ASC
                """,
                (job_id,),
            ).fetchall()
        return list(rows)

    def get_frame(self, frame_id: str) -> dict[str, Any] | None:
        with self.pool.connection() as conn:
            return conn.execute(
                """
                SELECT sf.id::text AS id,
                       sf.job_id::text AS job_id,
                       sf.frame_index,
                       sf.script_json,
                       sf.active_version_id::text AS active_version_id,
                       j.project_id::text AS project_id,
                       p.aspect_ratio,
                       p.style_preset
                FROM story_frames sf
                INNER JOIN story_jobs j ON j.id = sf.job_id
                INNER JOIN projects p ON p.id = j.project_id
                WHERE sf.id = %s
                """,
                (frame_id,),
            ).fetchone()

    def apply_frame_edits(
        self,
        job_id: str,
        edits: list[dict[str, Any]],
    ) -> list[FrameScript]:
        """Merge user frame script edits by id or frame_index inside a transaction."""
        with self.pool.connection() as conn:
            with conn.transaction():
                for patch in edits:
                    frame_id = patch.get("id") or patch.get("frame_id")
                    frame_index = patch.get("frame_index")
                    if frame_id:
                        row = conn.execute(
                            """
                            SELECT id::text AS id, frame_index, script_json
                            FROM story_frames
                            WHERE id = %s AND job_id = %s
                            """,
                            (frame_id, job_id),
                        ).fetchone()
                    elif frame_index is not None:
                        row = conn.execute(
                            """
                            SELECT id::text AS id, frame_index, script_json
                            FROM story_frames
                            WHERE job_id = %s AND frame_index = %s
                            """,
                            (job_id, int(frame_index)),
                        ).fetchone()
                    else:
                        row = None

                    if not row:
                        continue

                    merged = dict(row["script_json"] or {})
                    incoming = patch.get("script_json") if isinstance(patch.get("script_json"), dict) else patch
                    for k, v in incoming.items():
                        if k not in ("id", "frame_id"):
                            merged[k] = v
                    merged["frame_index"] = row["frame_index"]
                    conn.execute(
                        """
                        UPDATE story_frames
                        SET script_json = %s::jsonb
                        WHERE id = %s
                        """,
                        (json.dumps(merged, ensure_ascii=False), row["id"]),
                    )

        rows = self.list_frames(job_id)
        frames: list[FrameScript] = []
        for r in rows:
            payload = dict(r["script_json"] or {})
            payload["frame_index"] = r["frame_index"]
            payload["id"] = r["id"]
            frames.append(FrameScript.model_validate(payload))
        return frames

    def create_frame_version(
        self,
        frame_id: str,
        prompt_snapshot: str,
        image_url: str,
        user_edit_note: str | None = None,
        seed: int | None = None,
        vlm_score: float | None = None,
        vlm_feedback: dict[str, Any] | None = None,
    ) -> FrameVersionResponse:
        """Atomically insert next version_no for frame_id and set active_version_id."""
        with self.pool.connection() as conn:
            with conn.transaction():
                frame_row = conn.execute(
                    "SELECT id::text AS id FROM story_frames WHERE id = %s FOR UPDATE",
                    (frame_id,),
                ).fetchone()
                if frame_row is None:
                    raise LookupError(f"story frame not found: {frame_id}")

                max_row = conn.execute(
                    "SELECT COALESCE(MAX(version_no), 0) AS max_ver FROM frame_versions WHERE frame_id = %s",
                    (frame_id,),
                ).fetchone()
                next_ver = int(max_row["max_ver"]) + 1

                ver_row = conn.execute(
                    """
                    INSERT INTO frame_versions (
                        frame_id, version_no, user_edit_note, prompt_snapshot,
                        image_url, seed, vlm_score, vlm_feedback
                    )
                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s::jsonb)
                    RETURNING id::text AS id, frame_id::text AS frame_id, version_no,
                              user_edit_note, prompt_snapshot, image_url, seed,
                              vlm_score::float8 AS vlm_score, vlm_feedback
                    """,
                    (
                        frame_id,
                        next_ver,
                        user_edit_note,
                        prompt_snapshot,
                        image_url,
                        seed,
                        vlm_score,
                        json.dumps(vlm_feedback, ensure_ascii=False)
                        if vlm_feedback is not None
                        else None,
                    ),
                ).fetchone()
                conn.execute(
                    "UPDATE story_frames SET active_version_id = %s WHERE id = %s",
                    (ver_row["id"], frame_id),
                )
        return FrameVersionResponse.model_validate(ver_row)

    def set_character_reference_if_empty(self, project_id: str, image_url: str) -> None:
        """Backfill Frame 1 visual anchor into primary CHARACTER asset if reference_image_url is NULL."""
        if not project_id or not image_url:
            return
        with self.pool.connection() as conn:
            row = conn.execute(
                """
                SELECT id
                FROM asset_style_profiles
                WHERE project_id = %s
                  AND asset_type = 'CHARACTER'
                  AND reference_image_url IS NULL
                ORDER BY is_locked DESC, created_at ASC
                LIMIT 1
                """,
                (project_id,),
            ).fetchone()
            if row:
                conn.execute(
                    """
                    UPDATE asset_style_profiles
                    SET reference_image_url = %s
                    WHERE id = %s AND reference_image_url IS NULL
                    """,
                    (image_url, row["id"]),
                )


class InMemoryStoryRepository:
    """In-memory double of StoryRepository for DB-free unit testing."""

    def __init__(self, memory_repo: Any = None) -> None:
        self._memory_repo = memory_repo
        self.jobs: dict[str, dict[str, Any]] = {}
        self.frames: dict[str, dict[str, Any]] = {}
        self.versions: dict[str, list[FrameVersionResponse]] = {}

    def ensure_job(
        self,
        job_id: str,
        project_id: str,
        thread_id: str,
        aspect_ratio: str = "16:9",
        style_preset: str = "cinematic_melancholy",
    ) -> None:
        self.jobs.setdefault(
            job_id,
            {
                "id": job_id,
                "project_id": project_id,
                "langgraph_thread_id": thread_id,
                "status": "RUNNING",
                "current_stage": "EXTRACT_ASSETS",
                "aspect_ratio": aspect_ratio,
                "style_preset": style_preset,
            },
        )

    def get_job(self, job_id: str) -> dict[str, Any] | None:
        return self.jobs.get(job_id)

    def update_job_status(
        self,
        job_id: str,
        status: str,
        current_stage: str,
        error_message: str | None = None,
    ) -> None:
        if job_id in self.jobs:
            self.jobs[job_id]["status"] = status
            self.jobs[job_id]["current_stage"] = current_stage
            if error_message is not None:
                self.jobs[job_id]["error_message"] = error_message

    def upsert_story_frames(self, job_id: str, frames: list[FrameScript]) -> list[FrameScript]:
        saved: list[FrameScript] = []
        existing_by_index = {
            f["frame_index"]: f for f in self.frames.values() if f["job_id"] == job_id
        }
        job = self.jobs.get(job_id, {})
        for frame in frames:
            existing = existing_by_index.get(frame.frame_index)
            fid = existing["id"] if existing else (frame.id or str(uuid.uuid4()))
            script_dict = frame.model_dump(exclude={"id"}, mode="json")
            self.frames[fid] = {
                "id": fid,
                "job_id": job_id,
                "frame_index": frame.frame_index,
                "script_json": script_dict,
                "active_version_id": existing.get("active_version_id") if existing else None,
                "project_id": job.get("project_id", ""),
                "aspect_ratio": job.get("aspect_ratio", "16:9"),
                "style_preset": job.get("style_preset", "cinematic_melancholy"),
            }
            saved.append(frame.model_copy(update={"id": fid}))
        return saved

    def list_frames(self, job_id: str) -> list[dict[str, Any]]:
        rows = [f for f in self.frames.values() if f["job_id"] == job_id]
        rows.sort(key=lambda item: item["frame_index"])
        return rows

    def get_frame(self, frame_id: str) -> dict[str, Any] | None:
        return self.frames.get(frame_id)

    def apply_frame_edits(
        self,
        job_id: str,
        edits: list[dict[str, Any]],
    ) -> list[FrameScript]:
        job_frames = self.list_frames(job_id)
        by_id = {f["id"]: f for f in job_frames}
        by_idx = {f["frame_index"]: f for f in job_frames}
        for patch in edits:
            fid = patch.get("id") or patch.get("frame_id")
            fidx = patch.get("frame_index")
            target = by_id.get(fid) if fid else (by_idx.get(int(fidx)) if fidx is not None else None)
            if target is None:
                continue
            merged = dict(target["script_json"] or {})
            incoming = patch.get("script_json") if isinstance(patch.get("script_json"), dict) else patch
            for k, v in incoming.items():
                if k not in ("id", "frame_id"):
                    merged[k] = v
            merged["frame_index"] = target["frame_index"]
            target["script_json"] = merged

        out: list[FrameScript] = []
        for f in self.list_frames(job_id):
            payload = dict(f["script_json"] or {})
            payload["frame_index"] = f["frame_index"]
            payload["id"] = f["id"]
            out.append(FrameScript.model_validate(payload))
        return out

    def create_frame_version(
        self,
        frame_id: str,
        prompt_snapshot: str,
        image_url: str,
        user_edit_note: str | None = None,
        seed: int | None = None,
        vlm_score: float | None = None,
        vlm_feedback: dict[str, Any] | None = None,
    ) -> FrameVersionResponse:
        frame = self.frames.get(frame_id)
        if frame is None:
            raise LookupError(f"story frame not found: {frame_id}")
        bucket = self.versions.setdefault(frame_id, [])
        next_ver = len(bucket) + 1
        vid = str(uuid.uuid4())
        ver = FrameVersionResponse(
            id=vid,
            frame_id=frame_id,
            version_no=next_ver,
            user_edit_note=user_edit_note,
            prompt_snapshot=prompt_snapshot,
            image_url=image_url,
            seed=seed,
            vlm_score=vlm_score,
            vlm_feedback=vlm_feedback,
        )
        bucket.append(ver)
        frame["active_version_id"] = vid
        if (
            frame.get("frame_index") == 1
            and self._memory_repo is not None
            and hasattr(self._memory_repo, "set_first_frame_reference_image")
        ):
            self._memory_repo.set_first_frame_reference_image(frame["job_id"], image_url)
        return ver

    def set_character_reference_if_empty(self, project_id: str, image_url: str) -> None:
        if self._memory_repo is None or not project_id or not image_url:
            return
        assets = self._memory_repo.list_project_assets(project_id)
        chars = [a for a in assets if a.asset_type == "CHARACTER" and not a.reference_image_url]
        chars.sort(key=lambda a: (not a.is_locked, a.entity_key))
        if chars:
            updated = chars[0].model_copy(update={"reference_image_url": image_url})
            self._memory_repo.upsert_assets(project_id, [updated])
