package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
)

// ProjectRepository defines persistence operations for projects.
type ProjectRepository interface {
	Create(ctx context.Context, req model.CreateProjectRequest) (model.Project, error)
	GetByID(ctx context.Context, id uuid.UUID) (model.Project, error)
	List(ctx context.Context, limit, offset int) ([]model.Project, int64, error)
	Update(ctx context.Context, id uuid.UUID, req model.UpdateProjectRequest) (model.Project, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// PostgresProjectRepository implements ProjectRepository backed by PostgreSQL.
type PostgresProjectRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresProjectRepository constructs a PostgresProjectRepository.
func NewPostgresProjectRepository(pool *pgxpool.Pool) *PostgresProjectRepository {
	return &PostgresProjectRepository{pool: pool}
}

func (r *PostgresProjectRepository) Create(ctx context.Context, req model.CreateProjectRequest) (model.Project, error) {
	const q = `
		INSERT INTO projects (title, raw_story, aspect_ratio, style_preset, target_frames)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, title, raw_story, aspect_ratio, style_preset, target_frames, created_at, updated_at`
	var p model.Project
	err := r.pool.QueryRow(ctx, q, req.Title, req.RawStory, req.AspectRatio, req.StylePreset, *req.TargetFrames).Scan(
		&p.ID, &p.Title, &p.RawStory, &p.AspectRatio, &p.StylePreset, &p.TargetFrames, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

func (r *PostgresProjectRepository) GetByID(ctx context.Context, id uuid.UUID) (model.Project, error) {
	const q = `
		SELECT id, title, raw_story, aspect_ratio, style_preset, target_frames, created_at, updated_at
		FROM projects
		WHERE id = $1`
	var p model.Project
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.Title, &p.RawStory, &p.AspectRatio, &p.StylePreset, &p.TargetFrames, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Project{}, ErrNotFound
	}
	return p, err
}

func (r *PostgresProjectRepository) List(ctx context.Context, limit, offset int) ([]model.Project, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM projects`).Scan(&total); err != nil {
		return nil, 0, err
	}

	const q = `
		SELECT id, title, raw_story, aspect_ratio, style_preset, target_frames, created_at, updated_at
		FROM projects
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2`
	rows, err := r.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]model.Project, 0)
	for rows.Next() {
		var p model.Project
		if err := rows.Scan(
			&p.ID, &p.Title, &p.RawStory, &p.AspectRatio, &p.StylePreset, &p.TargetFrames, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *PostgresProjectRepository) Update(ctx context.Context, id uuid.UUID, req model.UpdateProjectRequest) (model.Project, error) {
	const q = `
		UPDATE projects
		SET title         = COALESCE($2, title),
		    raw_story     = COALESCE($3, raw_story),
		    aspect_ratio  = COALESCE($4, aspect_ratio),
		    style_preset  = COALESCE($5, style_preset),
		    target_frames = COALESCE($6, target_frames)
		WHERE id = $1
		RETURNING id, title, raw_story, aspect_ratio, style_preset, target_frames, created_at, updated_at`
	var p model.Project
	err := r.pool.QueryRow(ctx, q, id, req.Title, req.RawStory, req.AspectRatio, req.StylePreset, req.TargetFrames).Scan(
		&p.ID, &p.Title, &p.RawStory, &p.AspectRatio, &p.StylePreset, &p.TargetFrames, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Project{}, ErrNotFound
	}
	return p, err
}

func (r *PostgresProjectRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
