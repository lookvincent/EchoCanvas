package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
)

// AssetRepository defines persistence operations for asset_style_profiles.
type AssetRepository interface {
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]model.AssetStyleProfile, error)
	Create(ctx context.Context, projectID uuid.UUID, req model.CreateAssetRequest) (model.AssetStyleProfile, error)
	GetByID(ctx context.Context, id uuid.UUID) (model.AssetStyleProfile, error)
	Update(ctx context.Context, id uuid.UUID, req model.UpdateAssetRequest) (model.AssetStyleProfile, error)
}

// PostgresAssetRepository implements AssetRepository backed by PostgreSQL.
type PostgresAssetRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresAssetRepository constructs a PostgresAssetRepository.
func NewPostgresAssetRepository(pool *pgxpool.Pool) *PostgresAssetRepository {
	return &PostgresAssetRepository{pool: pool}
}

func (r *PostgresAssetRepository) ListByProject(ctx context.Context, projectID uuid.UUID) ([]model.AssetStyleProfile, error) {
	const q = `
		SELECT id, project_id, asset_type, entity_key, display_name, profile_json, reference_image_url, source, is_locked, created_at, updated_at
		FROM asset_style_profiles
		WHERE project_id = $1
		ORDER BY created_at ASC`
	rows, err := r.pool.Query(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.AssetStyleProfile, 0)
	for rows.Next() {
		var a model.AssetStyleProfile
		if err := rows.Scan(
			&a.ID, &a.ProjectID, &a.AssetType, &a.EntityKey, &a.DisplayName,
			&a.ProfileJSON, &a.ReferenceImageURL, &a.Source, &a.IsLocked, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *PostgresAssetRepository) Create(ctx context.Context, projectID uuid.UUID, req model.CreateAssetRequest) (model.AssetStyleProfile, error) {
	const q = `
		INSERT INTO asset_style_profiles (project_id, asset_type, entity_key, display_name, profile_json, reference_image_url, source, is_locked)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, project_id, asset_type, entity_key, display_name, profile_json, reference_image_url, source, is_locked, created_at, updated_at`
	var a model.AssetStyleProfile
	err := r.pool.QueryRow(
		ctx, q,
		projectID, req.AssetType, req.EntityKey, req.DisplayName,
		[]byte(req.ProfileJSON), req.ReferenceImageURL, req.Source, *req.IsLocked,
	).Scan(
		&a.ID, &a.ProjectID, &a.AssetType, &a.EntityKey, &a.DisplayName,
		&a.ProfileJSON, &a.ReferenceImageURL, &a.Source, &a.IsLocked, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if IsUniqueViolation(err) {
			return model.AssetStyleProfile{}, ErrConflict
		}
		if IsForeignKeyViolation(err) {
			return model.AssetStyleProfile{}, ErrNotFound
		}
		return model.AssetStyleProfile{}, err
	}
	return a, nil
}

func (r *PostgresAssetRepository) GetByID(ctx context.Context, id uuid.UUID) (model.AssetStyleProfile, error) {
	const q = `
		SELECT id, project_id, asset_type, entity_key, display_name, profile_json, reference_image_url, source, is_locked, created_at, updated_at
		FROM asset_style_profiles
		WHERE id = $1`
	var a model.AssetStyleProfile
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&a.ID, &a.ProjectID, &a.AssetType, &a.EntityKey, &a.DisplayName,
		&a.ProfileJSON, &a.ReferenceImageURL, &a.Source, &a.IsLocked, &a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.AssetStyleProfile{}, ErrNotFound
	}
	return a, err
}

// Update modifies an asset profile and enforces source='user_ground_truth' (FR-B02).
func (r *PostgresAssetRepository) Update(ctx context.Context, id uuid.UUID, req model.UpdateAssetRequest) (model.AssetStyleProfile, error) {
	var profileBytes []byte
	if req.ProfileJSON != nil {
		profileBytes = *req.ProfileJSON
	}

	const q = `
		UPDATE asset_style_profiles
		SET display_name        = COALESCE($2, display_name),
		    profile_json        = COALESCE($3, profile_json),
		    reference_image_url = COALESCE($4, reference_image_url),
		    is_locked           = COALESCE($5, is_locked),
		    source              = 'user_ground_truth'
		WHERE id = $1
		RETURNING id, project_id, asset_type, entity_key, display_name, profile_json, reference_image_url, source, is_locked, created_at, updated_at`
	var a model.AssetStyleProfile
	err := r.pool.QueryRow(ctx, q, id, req.DisplayName, profileBytes, req.ReferenceImageURL, req.IsLocked).Scan(
		&a.ID, &a.ProjectID, &a.AssetType, &a.EntityKey, &a.DisplayName,
		&a.ProfileJSON, &a.ReferenceImageURL, &a.Source, &a.IsLocked, &a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.AssetStyleProfile{}, ErrNotFound
	}
	return a, err
}
