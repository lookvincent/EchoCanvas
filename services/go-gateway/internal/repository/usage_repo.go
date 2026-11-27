package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
)

var ErrBudgetTooLow = errors.New("approved budget must exceed current total cost")

// UsageRepository persists AIGC ledger entries and enforces job budget updates.
type UsageRepository interface {
	RecordUsage(ctx context.Context, entry model.UsageLedgerEntry) (model.UsageLedgerEntry, float64, error)
	GetJobBudgetSnapshot(ctx context.Context, jobID uuid.UUID) (model.JobBudgetSnapshot, error)
	MarkJobPausedBudget(ctx context.Context, jobID uuid.UUID) error
	ApproveJobBudget(ctx context.Context, jobID uuid.UUID, newLimitUSD float64) (model.StoryJob, error)
	GetJobUsageSummary(ctx context.Context, jobID uuid.UUID) (model.JobUsageSummary, error)
}

// PostgresUsageRepository implements UsageRepository backed by PostgreSQL.
type PostgresUsageRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresUsageRepository constructs a PostgresUsageRepository.
func NewPostgresUsageRepository(pool *pgxpool.Pool) *PostgresUsageRepository {
	return &PostgresUsageRepository{pool: pool}
}

func (r *PostgresUsageRepository) RecordUsage(ctx context.Context, entry model.UsageLedgerEntry) (model.UsageLedgerEntry, float64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.UsageLedgerEntry{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insQ = `
		INSERT INTO usage_ledger (job_id, frame_id, trace_id, provider, model, call_type, input_tokens, output_tokens, image_count, latency_ms, cost_usd, is_fallback)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, job_id, frame_id, trace_id, provider, model, call_type, input_tokens, output_tokens, image_count, latency_ms, cost_usd, is_fallback, created_at`

	var saved model.UsageLedgerEntry
	err = tx.QueryRow(
		ctx, insQ,
		entry.JobID, entry.FrameID, entry.TraceID, entry.Provider, entry.Model, entry.CallType,
		entry.InputTokens, entry.OutputTokens, entry.ImageCount, entry.LatencyMS, entry.CostUSD, entry.IsFallback,
	).Scan(
		&saved.ID, &saved.JobID, &saved.FrameID, &saved.TraceID, &saved.Provider, &saved.Model, &saved.CallType,
		&saved.InputTokens, &saved.OutputTokens, &saved.ImageCount, &saved.LatencyMS, &saved.CostUSD, &saved.IsFallback, &saved.CreatedAt,
	)
	if err != nil {
		if IsForeignKeyViolation(err) {
			return model.UsageLedgerEntry{}, 0, ErrNotFound
		}
		return model.UsageLedgerEntry{}, 0, err
	}

	var totalCost float64
	const updQ = `
		UPDATE story_jobs
		SET total_cost_usd = total_cost_usd + $2
		WHERE id = $1
		RETURNING total_cost_usd`
	if err := tx.QueryRow(ctx, updQ, entry.JobID, entry.CostUSD).Scan(&totalCost); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.UsageLedgerEntry{}, 0, ErrNotFound
		}
		return model.UsageLedgerEntry{}, 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.UsageLedgerEntry{}, 0, err
	}
	return saved, totalCost, nil
}

func (r *PostgresUsageRepository) GetJobBudgetSnapshot(ctx context.Context, jobID uuid.UUID) (model.JobBudgetSnapshot, error) {
	const q = `
		SELECT j.id,
		       j.status,
		       j.current_stage,
		       j.budget_limit_usd,
		       COALESCE((SELECT SUM(u.cost_usd) FROM usage_ledger u WHERE u.job_id = j.id), 0) AS total_cost_usd
		FROM story_jobs j
		WHERE j.id = $1`
	var snap model.JobBudgetSnapshot
	err := r.pool.QueryRow(ctx, q, jobID).Scan(
		&snap.JobID, &snap.Status, &snap.CurrentStage, &snap.BudgetLimitUSD, &snap.TotalCostUSD,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.JobBudgetSnapshot{}, ErrNotFound
	}
	return snap, err
}

func (r *PostgresUsageRepository) MarkJobPausedBudget(ctx context.Context, jobID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `UPDATE story_jobs SET status = 'PAUSED_BUDGET' WHERE id = $1`, jobID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresUsageRepository) ApproveJobBudget(ctx context.Context, jobID uuid.UUID, newLimitUSD float64) (model.StoryJob, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.StoryJob{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentCost float64
	err = tx.QueryRow(ctx, `SELECT total_cost_usd FROM story_jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(&currentCost)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoryJob{}, ErrNotFound
	}
	if err != nil {
		return model.StoryJob{}, err
	}
	if newLimitUSD <= currentCost {
		return model.StoryJob{}, ErrBudgetTooLow
	}

	const updQ = `
		UPDATE story_jobs
		SET budget_limit_usd = $2,
		    status = CASE WHEN status = 'PAUSED_BUDGET' THEN 'RUNNING' ELSE status END
		WHERE id = $1
		RETURNING id, project_id, idempotency_key, langgraph_thread_id, trace_id, status, current_stage, budget_limit_usd, total_cost_usd, error_message, created_at, updated_at`
	job, err := scanJob(tx.QueryRow(ctx, updQ, jobID, newLimitUSD))
	if err != nil {
		return model.StoryJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.StoryJob{}, err
	}
	return job, nil
}

func (r *PostgresUsageRepository) GetJobUsageSummary(ctx context.Context, jobID uuid.UUID) (model.JobUsageSummary, error) {
	snap, err := r.GetJobBudgetSnapshot(ctx, jobID)
	if err != nil {
		return model.JobUsageSummary{}, err
	}

	const entriesQ = `
		SELECT id, job_id, frame_id, trace_id, provider, model, call_type, input_tokens, output_tokens, image_count, latency_ms, cost_usd, is_fallback, created_at
		FROM usage_ledger
		WHERE job_id = $1
		ORDER BY created_at ASC`
	rows, err := r.pool.Query(ctx, entriesQ, jobID)
	if err != nil {
		return model.JobUsageSummary{}, err
	}
	defer rows.Close()

	entries := make([]model.UsageLedgerEntry, 0)
	for rows.Next() {
		var e model.UsageLedgerEntry
		if err := rows.Scan(
			&e.ID, &e.JobID, &e.FrameID, &e.TraceID, &e.Provider, &e.Model, &e.CallType,
			&e.InputTokens, &e.OutputTokens, &e.ImageCount, &e.LatencyMS, &e.CostUSD, &e.IsFallback, &e.CreatedAt,
		); err != nil {
			return model.JobUsageSummary{}, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return model.JobUsageSummary{}, err
	}

	const breakdownQ = `
		SELECT provider, model, call_type, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(image_count), 0), COALESCE(SUM(cost_usd), 0)
		FROM usage_ledger
		WHERE job_id = $1
		GROUP BY provider, model, call_type
		ORDER BY provider ASC, model ASC`
	bRows, err := r.pool.Query(ctx, breakdownQ, jobID)
	if err != nil {
		return model.JobUsageSummary{}, err
	}
	defer bRows.Close()

	breakdown := make([]model.ModelUsageBreakdown, 0)
	for bRows.Next() {
		var b model.ModelUsageBreakdown
		if err := bRows.Scan(
			&b.Provider, &b.Model, &b.CallType, &b.CallCount,
			&b.InputTokens, &b.OutputTokens, &b.ImageCount, &b.CostUSD,
		); err != nil {
			return model.JobUsageSummary{}, err
		}
		breakdown = append(breakdown, b)
	}
	if err := bRows.Err(); err != nil {
		return model.JobUsageSummary{}, err
	}

	return model.JobUsageSummary{
		JobID:          snap.JobID,
		Status:         snap.Status,
		BudgetLimitUSD: snap.BudgetLimitUSD,
		TotalCostUSD:   snap.TotalCostUSD,
		Breakdown:      breakdown,
		Entries:        entries,
	}, nil
}
