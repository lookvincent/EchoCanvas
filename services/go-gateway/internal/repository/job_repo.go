package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
)

var ErrInvalidTransition = errors.New("invalid status transition")

// JobRepository manages story_jobs, story_frames, frame_versions, and frame_tasks.
type JobRepository interface {
	CreateOrGetByIdempotencyKey(ctx context.Context, projectID uuid.UUID, idempotencyKey, threadID, traceID string, budgetLimitUSD float64) (model.StoryJob, bool, error)
	GetJobByID(ctx context.Context, jobID uuid.UUID) (model.StoryJob, error)
	UpdateJobStatus(ctx context.Context, jobID uuid.UUID, status, stage string, errMsg *string) error
	CreateFrame(ctx context.Context, jobID uuid.UUID, frameIndex int, scriptJSON json.RawMessage) (model.StoryFrame, error)
	CreateFrameTask(ctx context.Context, jobID, frameID uuid.UUID, taskType string, maxRetries int) (model.FrameTask, error)
	GetFrameByID(ctx context.Context, frameID uuid.UUID) (model.StoryFrame, error)
	SetFrameActiveVersion(ctx context.Context, frameID, versionID uuid.UUID) (model.StoryFrame, error)
	ListFrameVersions(ctx context.Context, frameID uuid.UUID) ([]model.FrameVersion, error)
	GetFrameTaskByID(ctx context.Context, taskID uuid.UUID) (model.FrameTask, error)
	ListFramesByJob(ctx context.Context, jobID uuid.UUID) ([]model.StoryFrame, error)
	ListVersionsByJob(ctx context.Context, jobID uuid.UUID) ([]model.FrameVersion, error)
	ListTasksByJob(ctx context.Context, jobID uuid.UUID) ([]model.FrameTask, error)
	TransitionTaskStatus(ctx context.Context, taskID uuid.UUID, fromStatus, toStatus string, errCode, errDetail *string) (model.FrameTask, error)
	IncrementTaskRetry(ctx context.Context, taskID uuid.UUID, toStatus string, errCode, errDetail *string) (model.FrameTask, error)
	CheckAndFinalizeJobStatus(ctx context.Context, jobID uuid.UUID) (model.StoryJob, bool, error)
}

// PostgresJobRepository implements JobRepository backed by PostgreSQL.
type PostgresJobRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresJobRepository constructs a PostgresJobRepository.
func NewPostgresJobRepository(pool *pgxpool.Pool) *PostgresJobRepository {
	return &PostgresJobRepository{pool: pool}
}

func scanJob(row pgx.Row) (model.StoryJob, error) {
	var j model.StoryJob
	err := row.Scan(
		&j.ID, &j.ProjectID, &j.IdempotencyKey, &j.LangGraphThreadID, &j.TraceID,
		&j.Status, &j.CurrentStage, &j.BudgetLimitUSD, &j.TotalCostUSD, &j.ErrorMessage,
		&j.CreatedAt, &j.UpdatedAt,
	)
	return j, err
}

func scanTask(row pgx.Row) (model.FrameTask, error) {
	var t model.FrameTask
	err := row.Scan(
		&t.ID, &t.JobID, &t.FrameID, &t.TaskType, &t.Status,
		&t.RetryCount, &t.MaxRetries, &t.ErrorCode, &t.ErrorDetail,
		&t.StartedAt, &t.FinishedAt, &t.CreatedAt,
	)
	return t, err
}

func (r *PostgresJobRepository) CreateOrGetByIdempotencyKey(
	ctx context.Context,
	projectID uuid.UUID,
	idempotencyKey, threadID, traceID string,
	budgetLimitUSD float64,
) (model.StoryJob, bool, error) {
	const insertQ = `
		INSERT INTO story_jobs (project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage, budget_limit_usd)
		VALUES ($1, $2, $3, $4, 'PENDING', 'INIT', $5)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage, budget_limit_usd, total_cost_usd, error_message, created_at, updated_at`

	job, err := scanJob(r.pool.QueryRow(ctx, insertQ, projectID, idempotencyKey, threadID, traceID, budgetLimitUSD))
	if err == nil {
		return job, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if IsForeignKeyViolation(err) {
			return model.StoryJob{}, false, ErrNotFound
		}
		return model.StoryJob{}, false, err
	}

	const selectQ = `
		SELECT id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage, budget_limit_usd, total_cost_usd, error_message, created_at, updated_at
		FROM story_jobs
		WHERE idempotency_key = $1`
	existing, err := scanJob(r.pool.QueryRow(ctx, selectQ, idempotencyKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.StoryJob{}, false, ErrNotFound
		}
		return model.StoryJob{}, false, err
	}
	if existing.ProjectID != projectID {
		return model.StoryJob{}, false, ErrConflict
	}
	return existing, false, nil
}

func (r *PostgresJobRepository) GetJobByID(ctx context.Context, jobID uuid.UUID) (model.StoryJob, error) {
	const q = `
		SELECT id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage, budget_limit_usd, total_cost_usd, error_message, created_at, updated_at
		FROM story_jobs
		WHERE id = $1`
	job, err := scanJob(r.pool.QueryRow(ctx, q, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoryJob{}, ErrNotFound
	}
	return job, err
}

func (r *PostgresJobRepository) UpdateJobStatus(ctx context.Context, jobID uuid.UUID, status, stage string, errMsg *string) error {
	const q = `
		UPDATE story_jobs
		SET status = $2,
		    current_stage = $3,
		    error_message = COALESCE($4, error_message)
		WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, jobID, status, stage, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresJobRepository) CreateFrame(ctx context.Context, jobID uuid.UUID, frameIndex int, scriptJSON json.RawMessage) (model.StoryFrame, error) {
	const q = `
		INSERT INTO story_frames (job_id, frame_index, script_json)
		VALUES ($1, $2, $3)
		RETURNING id, job_id, frame_index, script_json, active_version_id, created_at, updated_at`
	var f model.StoryFrame
	err := r.pool.QueryRow(ctx, q, jobID, frameIndex, []byte(scriptJSON)).Scan(
		&f.ID, &f.JobID, &f.FrameIndex, &f.ScriptJSON, &f.ActiveVersionID, &f.CreatedAt, &f.UpdatedAt,
	)
	if err != nil {
		if IsUniqueViolation(err) {
			return model.StoryFrame{}, ErrConflict
		}
		if IsForeignKeyViolation(err) {
			return model.StoryFrame{}, ErrNotFound
		}
		return model.StoryFrame{}, err
	}
	return f, nil
}

func (r *PostgresJobRepository) CreateFrameTask(ctx context.Context, jobID, frameID uuid.UUID, taskType string, maxRetries int) (model.FrameTask, error) {
	const q = `
		INSERT INTO frame_tasks (job_id, frame_id, task_type, status, max_retries)
		VALUES ($1, $2, $3, 'PENDING', $4)
		RETURNING id, job_id, frame_id, task_type, status, retry_count, max_retries, error_code, error_detail, started_at, finished_at, created_at`
	t, err := scanTask(r.pool.QueryRow(ctx, q, jobID, frameID, taskType, maxRetries))
	if err != nil {
		if IsForeignKeyViolation(err) {
			return model.FrameTask{}, ErrNotFound
		}
		return model.FrameTask{}, err
	}
	return t, nil
}

func (r *PostgresJobRepository) GetFrameTaskByID(ctx context.Context, taskID uuid.UUID) (model.FrameTask, error) {
	const q = `
		SELECT id, job_id, frame_id, task_type, status, retry_count, max_retries, error_code, error_detail, started_at, finished_at, created_at
		FROM frame_tasks
		WHERE id = $1`
	t, err := scanTask(r.pool.QueryRow(ctx, q, taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.FrameTask{}, ErrNotFound
	}
	return t, err
}

func (r *PostgresJobRepository) ListFramesByJob(ctx context.Context, jobID uuid.UUID) ([]model.StoryFrame, error) {
	const q = `
		SELECT id, job_id, frame_index, script_json, active_version_id, created_at, updated_at
		FROM story_frames
		WHERE job_id = $1
		ORDER BY frame_index ASC`
	rows, err := r.pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.StoryFrame, 0)
	for rows.Next() {
		var f model.StoryFrame
		if err := rows.Scan(&f.ID, &f.JobID, &f.FrameIndex, &f.ScriptJSON, &f.ActiveVersionID, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

func (r *PostgresJobRepository) ListVersionsByJob(ctx context.Context, jobID uuid.UUID) ([]model.FrameVersion, error) {
	const q = `
		SELECT fv.id, fv.frame_id, fv.version_no, fv.user_edit_note, fv.prompt_snapshot, fv.image_url, fv.seed, fv.vlm_score, fv.vlm_feedback, fv.created_at
		FROM frame_versions fv
		INNER JOIN story_frames sf ON sf.id = fv.frame_id
		WHERE sf.job_id = $1
		ORDER BY sf.frame_index ASC, fv.version_no ASC`
	rows, err := r.pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.FrameVersion, 0)
	for rows.Next() {
		var v model.FrameVersion
		if err := rows.Scan(
			&v.ID, &v.FrameID, &v.VersionNo, &v.UserEditNote, &v.PromptSnapshot,
			&v.ImageURL, &v.Seed, &v.VLMScore, &v.VLMFeedback, &v.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (r *PostgresJobRepository) ListTasksByJob(ctx context.Context, jobID uuid.UUID) ([]model.FrameTask, error) {
	const q = `
		SELECT id, job_id, frame_id, task_type, status, retry_count, max_retries, error_code, error_detail, started_at, finished_at, created_at
		FROM frame_tasks
		WHERE job_id = $1
		ORDER BY created_at ASC`
	rows, err := r.pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.FrameTask, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

// TransitionTaskStatus atomically transitions a task status using optimistic locking.
func (r *PostgresJobRepository) TransitionTaskStatus(
	ctx context.Context,
	taskID uuid.UUID,
	fromStatus, toStatus string,
	errCode, errDetail *string,
) (model.FrameTask, error) {
	const q = `
		UPDATE frame_tasks
		SET status       = $3::text,
		    error_code   = CASE WHEN $3::text = 'QUEUED' THEN NULL ELSE COALESCE($4::text, error_code) END,
		    error_detail = CASE WHEN $3::text = 'QUEUED' THEN NULL ELSE COALESCE($5::text, error_detail) END,
		    started_at   = CASE WHEN $3::text = 'RUNNING' THEN NOW() ELSE started_at END,
		    finished_at  = CASE WHEN $3::text IN ('SUCCEEDED', 'FAILED') THEN NOW() WHEN $3::text = 'QUEUED' THEN NULL ELSE finished_at END
		WHERE id = $1 AND status = $2::text
		RETURNING id, job_id, frame_id, task_type, status, retry_count, max_retries, error_code, error_detail, started_at, finished_at, created_at`
	t, err := scanTask(r.pool.QueryRow(ctx, q, taskID, fromStatus, toStatus, errCode, errDetail))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := r.GetFrameTaskByID(ctx, taskID); errors.Is(getErr, ErrNotFound) {
			return model.FrameTask{}, ErrNotFound
		}
		return model.FrameTask{}, ErrInvalidTransition
	}
	return t, err
}

func (r *PostgresJobRepository) IncrementTaskRetry(
	ctx context.Context,
	taskID uuid.UUID,
	toStatus string,
	errCode, errDetail *string,
) (model.FrameTask, error) {
	const q = `
		UPDATE frame_tasks
		SET retry_count  = retry_count + 1,
		    status       = $2,
		    error_code   = $3,
		    error_detail = $4
		WHERE id = $1
		RETURNING id, job_id, frame_id, task_type, status, retry_count, max_retries, error_code, error_detail, started_at, finished_at, created_at`
	t, err := scanTask(r.pool.QueryRow(ctx, q, taskID, toStatus, errCode, errDetail))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.FrameTask{}, ErrNotFound
	}
	return t, err
}

func (r *PostgresJobRepository) GetFrameByID(ctx context.Context, frameID uuid.UUID) (model.StoryFrame, error) {
	const q = `
		SELECT id, job_id, frame_index, script_json, active_version_id, created_at, updated_at
		FROM story_frames
		WHERE id = $1`
	var f model.StoryFrame
	err := r.pool.QueryRow(ctx, q, frameID).Scan(
		&f.ID, &f.JobID, &f.FrameIndex, &f.ScriptJSON, &f.ActiveVersionID, &f.CreatedAt, &f.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoryFrame{}, ErrNotFound
	}
	return f, err
}

func (r *PostgresJobRepository) ListFrameVersions(ctx context.Context, frameID uuid.UUID) ([]model.FrameVersion, error) {
	const q = `
		SELECT id, frame_id, version_no, user_edit_note, prompt_snapshot, image_url, seed, vlm_score, vlm_feedback, created_at
		FROM frame_versions
		WHERE frame_id = $1
		ORDER BY version_no ASC`
	rows, err := r.pool.Query(ctx, q, frameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.FrameVersion, 0)
	for rows.Next() {
		var v model.FrameVersion
		if err := rows.Scan(
			&v.ID, &v.FrameID, &v.VersionNo, &v.UserEditNote, &v.PromptSnapshot,
			&v.ImageURL, &v.Seed, &v.VLMScore, &v.VLMFeedback, &v.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (r *PostgresJobRepository) SetFrameActiveVersion(ctx context.Context, frameID, versionID uuid.UUID) (model.StoryFrame, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.StoryFrame{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM frame_versions WHERE id = $1 AND frame_id = $2)`,
		versionID, frameID,
	).Scan(&exists); err != nil {
		return model.StoryFrame{}, err
	}
	if !exists {
		return model.StoryFrame{}, ErrNotFound
	}

	const q = `
		UPDATE story_frames
		SET active_version_id = $2
		WHERE id = $1
		RETURNING id, job_id, frame_index, script_json, active_version_id, created_at, updated_at`
	var f model.StoryFrame
	err = tx.QueryRow(ctx, q, frameID, versionID).Scan(
		&f.ID, &f.JobID, &f.FrameIndex, &f.ScriptJSON, &f.ActiveVersionID, &f.CreatedAt, &f.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoryFrame{}, ErrNotFound
	}
	if err != nil {
		return model.StoryFrame{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.StoryFrame{}, err
	}
	return f, nil
}

func (r *PostgresJobRepository) CheckAndFinalizeJobStatus(ctx context.Context, jobID uuid.UUID) (model.StoryJob, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.StoryJob{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const lockQ = `
		SELECT id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage, budget_limit_usd, total_cost_usd, error_message, created_at, updated_at
		FROM story_jobs
		WHERE id = $1
		FOR UPDATE`
	job, err := scanJob(tx.QueryRow(ctx, lockQ, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoryJob{}, false, ErrNotFound
	}
	if err != nil {
		return model.StoryJob{}, false, err
	}
	if job.CurrentStage == model.JobStageDone {
		return job, false, nil
	}

	var frameCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM story_frames WHERE job_id = $1`, jobID).Scan(&frameCount); err != nil {
		return model.StoryJob{}, false, err
	}
	if frameCount == 0 {
		return job, false, nil
	}

	var distinctFrames, activeCount, succeededCount, failedCount int
	const statsQ = `
		SELECT COUNT(DISTINCT frame_id),
		       COALESCE(SUM(CASE WHEN status IN ('PENDING','QUEUED','RUNNING','VERIFYING') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'SUCCEEDED' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'FAILED' THEN 1 ELSE 0 END), 0)
		FROM frame_tasks
		WHERE job_id = $1 AND task_type = 'INITIAL_RENDER'`
	if err := tx.QueryRow(ctx, statsQ, jobID).Scan(&distinctFrames, &activeCount, &succeededCount, &failedCount); err != nil {
		return model.StoryJob{}, false, err
	}
	if distinctFrames < frameCount || activeCount > 0 || (succeededCount+failedCount) == 0 {
		return job, false, nil
	}

	finalStatus := model.JobStatusSucceeded
	if failedCount > 0 && succeededCount > 0 {
		finalStatus = model.JobStatusPartialFailed
	} else if failedCount > 0 {
		finalStatus = model.JobStatusFailed
	}

	const updQ = `
		UPDATE story_jobs
		SET status = $2::text,
		    current_stage = 'DONE'
		WHERE id = $1
		RETURNING id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage, budget_limit_usd, total_cost_usd, error_message, created_at, updated_at`
	updated, err := scanJob(tx.QueryRow(ctx, updQ, jobID, finalStatus))
	if err != nil {
		return model.StoryJob{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.StoryJob{}, false, err
	}
	return updated, true, nil
}
