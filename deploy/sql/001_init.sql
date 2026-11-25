-- EchoCanvas PostgreSQL 16 Initialization Schema
-- Aligns with docs/03-TDD.md Section 3.2 (8 core tables + extensions + indexes + triggers)

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS vector;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 1. Projects (FR-A01, FR-A02)
CREATE TABLE IF NOT EXISTS projects (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    title           VARCHAR(120) NOT NULL,
    raw_story       TEXT NOT NULL,
    aspect_ratio    VARCHAR(16) NOT NULL DEFAULT '16:9',
    style_preset    VARCHAR(64) NOT NULL DEFAULT 'cinematic_melancholy',
    target_frames   INT NOT NULL DEFAULT 4 CHECK (target_frames BETWEEN 2 AND 10),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DROP TRIGGER IF EXISTS trg_projects_updated_at ON projects;
CREATE TRIGGER trg_projects_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- 2. Global Asset & Style Memory (FR-B01~B04)
CREATE TABLE IF NOT EXISTS asset_style_profiles (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    asset_type          VARCHAR(32) NOT NULL CHECK (asset_type IN ('CHARACTER', 'SCENE', 'STYLE')),
    entity_key          VARCHAR(64) NOT NULL,
    display_name        VARCHAR(120) NOT NULL,
    profile_json        JSONB NOT NULL,
    reference_image_url TEXT,
    source              VARCHAR(32) NOT NULL DEFAULT 'agent_inferred' CHECK (source IN ('user_ground_truth', 'agent_inferred')),
    is_locked           BOOLEAN NOT NULL DEFAULT FALSE,
    embedding           VECTOR(1536),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, entity_key)
);

CREATE INDEX IF NOT EXISTS idx_assets_project ON asset_style_profiles(project_id);
CREATE INDEX IF NOT EXISTS idx_assets_embedding ON asset_style_profiles USING hnsw (embedding vector_cosine_ops);

DROP TRIGGER IF EXISTS trg_assets_updated_at ON asset_style_profiles;
CREATE TRIGGER trg_assets_updated_at
    BEFORE UPDATE ON asset_style_profiles
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- 3. Story Asynchronous Jobs (FR-A04, FR-F01)
CREATE TABLE IF NOT EXISTS story_jobs (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    idempotency_key     VARCHAR(128) NOT NULL UNIQUE,
    langgraph_thread_id VARCHAR(128) NOT NULL UNIQUE,
    trace_id            VARCHAR(64) NOT NULL,
    status              VARCHAR(32) NOT NULL DEFAULT 'PENDING' CHECK (
        status IN ('PENDING', 'RUNNING', 'WAITING_GATE', 'SUCCEEDED', 'PARTIAL_FAILED', 'FAILED', 'PAUSED_BUDGET')
    ),
    current_stage       VARCHAR(32) NOT NULL DEFAULT 'INIT' CHECK (
        current_stage IN ('INIT', 'EXTRACT_ASSETS', 'GATE_1', 'PLAN_SCRIPT', 'GATE_2', 'RENDER_FRAMES', 'DONE')
    ),
    budget_limit_usd    NUMERIC(10, 4) NOT NULL DEFAULT 0.5000,
    total_cost_usd      NUMERIC(10, 6) NOT NULL DEFAULT 0.000000,
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_jobs_project_created ON story_jobs(project_id, created_at DESC);

DROP TRIGGER IF EXISTS trg_story_jobs_updated_at ON story_jobs;
CREATE TRIGGER trg_story_jobs_updated_at
    BEFORE UPDATE ON story_jobs
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- 4. Storyboard Frames (FR-C01, FR-C02)
CREATE TABLE IF NOT EXISTS story_frames (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    job_id            UUID NOT NULL REFERENCES story_jobs(id) ON DELETE CASCADE,
    frame_index       INT NOT NULL,
    script_json       JSONB NOT NULL,
    active_version_id UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (job_id, frame_index)
);

DROP TRIGGER IF EXISTS trg_story_frames_updated_at ON story_frames;
CREATE TRIGGER trg_story_frames_updated_at
    BEFORE UPDATE ON story_frames
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- 5. Frame Visual Versions (FR-D01, FR-E02)
CREATE TABLE IF NOT EXISTS frame_versions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    frame_id        UUID NOT NULL REFERENCES story_frames(id) ON DELETE CASCADE,
    version_no      INT NOT NULL,
    user_edit_note  TEXT,
    prompt_snapshot TEXT NOT NULL,
    image_url       TEXT NOT NULL,
    seed            BIGINT,
    vlm_score       NUMERIC(4, 2),
    vlm_feedback    JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (frame_id, version_no)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_story_frames_active_version'
    ) THEN
        ALTER TABLE story_frames
            ADD CONSTRAINT fk_story_frames_active_version
            FOREIGN KEY (active_version_id) REFERENCES frame_versions(id) ON DELETE SET NULL;
    END IF;
END $$;

-- 6. Frame Asynchronous Execution Sub-Tasks (FR-D03, FR-D04)
CREATE TABLE IF NOT EXISTS frame_tasks (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    job_id          UUID NOT NULL REFERENCES story_jobs(id) ON DELETE CASCADE,
    frame_id        UUID NOT NULL REFERENCES story_frames(id) ON DELETE CASCADE,
    task_type       VARCHAR(32) NOT NULL CHECK (task_type IN ('INITIAL_RENDER', 'REGENERATE')),
    status          VARCHAR(32) NOT NULL DEFAULT 'PENDING' CHECK (
        status IN ('PENDING', 'QUEUED', 'RUNNING', 'VERIFYING', 'SUCCEEDED', 'FAILED')
    ),
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 2,
    error_code      VARCHAR(64),
    error_detail    TEXT,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_frame_tasks_job_status ON frame_tasks(job_id, status);

-- 7. Episodic Entity State Snapshots (FR-C04, FR-E03)
CREATE TABLE IF NOT EXISTS episodic_entity_states (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    job_id          UUID NOT NULL REFERENCES story_jobs(id) ON DELETE CASCADE,
    frame_index     INT NOT NULL,
    entity_key      VARCHAR(64) NOT NULL,
    state_json      JSONB NOT NULL,
    diff_from_prev  JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (job_id, frame_index, entity_key)
);

-- 8. AIGC Usage & Cost Ledger (FR-F02, FR-A04)
CREATE TABLE IF NOT EXISTS usage_ledger (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    job_id          UUID NOT NULL REFERENCES story_jobs(id) ON DELETE CASCADE,
    frame_id        UUID,
    trace_id        VARCHAR(64) NOT NULL,
    provider        VARCHAR(64) NOT NULL,
    model           VARCHAR(64) NOT NULL,
    call_type       VARCHAR(32) NOT NULL CHECK (call_type IN ('LLM_TEXT', 'IMAGE_GEN', 'VLM_JUDGE', 'EMBEDDING')),
    input_tokens    INT NOT NULL DEFAULT 0,
    output_tokens   INT NOT NULL DEFAULT 0,
    image_count     INT NOT NULL DEFAULT 0,
    latency_ms      INT NOT NULL DEFAULT 0,
    cost_usd        NUMERIC(10, 6) NOT NULL DEFAULT 0.000000,
    is_fallback     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_usage_job ON usage_ledger(job_id);
CREATE INDEX IF NOT EXISTS idx_usage_trace ON usage_ledger(trace_id);
