package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	validAspectRatios = map[string]bool{"16:9": true, "9:16": true, "1:1": true}
	validAssetTypes   = map[string]bool{"CHARACTER": true, "SCENE": true, "STYLE": true}
	validAssetSources = map[string]bool{"user_ground_truth": true, "agent_inferred": true}
)

// Project maps to the projects table.
type Project struct {
	ID           uuid.UUID `json:"id"`
	Title        string    `json:"title"`
	RawStory     string    `json:"raw_story"`
	AspectRatio  string    `json:"aspect_ratio"`
	StylePreset  string    `json:"style_preset"`
	TargetFrames int       `json:"target_frames"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreateProjectRequest defines payload for POST /api/v1/projects.
type CreateProjectRequest struct {
	Title        string `json:"title"`
	RawStory     string `json:"raw_story"`
	AspectRatio  string `json:"aspect_ratio"`
	StylePreset  string `json:"style_preset"`
	TargetFrames *int   `json:"target_frames"`
}

// NormalizeAndValidate applies defaults and validates creation inputs.
func (r *CreateProjectRequest) NormalizeAndValidate() error {
	r.Title = strings.TrimSpace(r.Title)
	r.RawStory = strings.TrimSpace(r.RawStory)
	r.AspectRatio = strings.TrimSpace(r.AspectRatio)
	r.StylePreset = strings.TrimSpace(r.StylePreset)

	if r.Title == "" || len([]rune(r.Title)) > 120 {
		return fmt.Errorf("title is required and must be at most 120 characters")
	}
	if r.RawStory == "" {
		return fmt.Errorf("raw_story is required")
	}
	if r.AspectRatio == "" {
		r.AspectRatio = "16:9"
	}
	if !validAspectRatios[r.AspectRatio] {
		return fmt.Errorf("aspect_ratio must be one of 16:9, 9:16, 1:1")
	}
	if r.StylePreset == "" {
		r.StylePreset = "cinematic_melancholy"
	}
	if len([]rune(r.StylePreset)) > 64 {
		return fmt.Errorf("style_preset must be at most 64 characters")
	}
	if r.TargetFrames == nil {
		v := 4
		r.TargetFrames = &v
	}
	if *r.TargetFrames < 2 || *r.TargetFrames > 10 {
		return fmt.Errorf("target_frames must be between 2 and 10")
	}
	return nil
}

// UpdateProjectRequest defines payload for PUT /api/v1/projects/:id.
type UpdateProjectRequest struct {
	Title        *string `json:"title"`
	RawStory     *string `json:"raw_story"`
	AspectRatio  *string `json:"aspect_ratio"`
	StylePreset  *string `json:"style_preset"`
	TargetFrames *int    `json:"target_frames"`
}

// Validate checks partial update fields for project.
func (r *UpdateProjectRequest) Validate() error {
	if r.Title == nil && r.RawStory == nil && r.AspectRatio == nil && r.StylePreset == nil && r.TargetFrames == nil {
		return fmt.Errorf("at least one field must be provided")
	}
	if r.Title != nil {
		v := strings.TrimSpace(*r.Title)
		if v == "" || len([]rune(v)) > 120 {
			return fmt.Errorf("title must be non-empty and at most 120 characters")
		}
		r.Title = &v
	}
	if r.RawStory != nil {
		v := strings.TrimSpace(*r.RawStory)
		if v == "" {
			return fmt.Errorf("raw_story cannot be empty")
		}
		r.RawStory = &v
	}
	if r.AspectRatio != nil {
		v := strings.TrimSpace(*r.AspectRatio)
		if !validAspectRatios[v] {
			return fmt.Errorf("aspect_ratio must be one of 16:9, 9:16, 1:1")
		}
		r.AspectRatio = &v
	}
	if r.StylePreset != nil {
		v := strings.TrimSpace(*r.StylePreset)
		if v == "" || len([]rune(v)) > 64 {
			return fmt.Errorf("style_preset must be non-empty and at most 64 characters")
		}
		r.StylePreset = &v
	}
	if r.TargetFrames != nil && (*r.TargetFrames < 2 || *r.TargetFrames > 10) {
		return fmt.Errorf("target_frames must be between 2 and 10")
	}
	return nil
}

// ProjectListResponse wraps paginated projects.
type ProjectListResponse struct {
	Items  []Project `json:"items"`
	Total  int64     `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

// AssetStyleProfile maps to the asset_style_profiles table.
type AssetStyleProfile struct {
	ID                uuid.UUID       `json:"id"`
	ProjectID         uuid.UUID       `json:"project_id"`
	AssetType         string          `json:"asset_type"`
	EntityKey         string          `json:"entity_key"`
	DisplayName       string          `json:"display_name"`
	ProfileJSON       json.RawMessage `json:"profile_json"`
	ReferenceImageURL *string         `json:"reference_image_url,omitempty"`
	Source            string          `json:"source"`
	IsLocked          bool            `json:"is_locked"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// CreateAssetRequest defines payload for POST /api/v1/projects/:id/assets.
type CreateAssetRequest struct {
	AssetType         string          `json:"asset_type"`
	EntityKey         string          `json:"entity_key"`
	DisplayName       string          `json:"display_name"`
	ProfileJSON       json.RawMessage `json:"profile_json"`
	ReferenceImageURL *string         `json:"reference_image_url"`
	Source            string          `json:"source"`
	IsLocked          *bool           `json:"is_locked"`
}

// NormalizeAndValidate validates asset creation inputs and applies defaults.
func (r *CreateAssetRequest) NormalizeAndValidate() error {
	r.AssetType = strings.ToUpper(strings.TrimSpace(r.AssetType))
	r.EntityKey = strings.TrimSpace(r.EntityKey)
	r.DisplayName = strings.TrimSpace(r.DisplayName)
	r.Source = strings.TrimSpace(r.Source)

	if !validAssetTypes[r.AssetType] {
		return fmt.Errorf("asset_type must be one of CHARACTER, SCENE, STYLE")
	}
	if r.EntityKey == "" || len([]rune(r.EntityKey)) > 64 {
		return fmt.Errorf("entity_key is required and must be at most 64 characters")
	}
	if r.DisplayName == "" || len([]rune(r.DisplayName)) > 120 {
		return fmt.Errorf("display_name is required and must be at most 120 characters")
	}
	if len(r.ProfileJSON) == 0 || !json.Valid(r.ProfileJSON) {
		return fmt.Errorf("profile_json must be valid JSON")
	}
	if r.Source == "" {
		r.Source = "agent_inferred"
	}
	if !validAssetSources[r.Source] {
		return fmt.Errorf("source must be user_ground_truth or agent_inferred")
	}
	if r.IsLocked == nil {
		locked := false
		r.IsLocked = &locked
	}
	return nil
}

// UpdateAssetRequest defines payload for PUT /api/v1/assets/:assetId.
type UpdateAssetRequest struct {
	DisplayName       *string          `json:"display_name"`
	ProfileJSON       *json.RawMessage `json:"profile_json"`
	ReferenceImageURL *string          `json:"reference_image_url"`
	IsLocked          *bool            `json:"is_locked"`
}

// Validate checks partial update fields for an asset profile.
func (r *UpdateAssetRequest) Validate() error {
	if r.DisplayName == nil && r.ProfileJSON == nil && r.ReferenceImageURL == nil && r.IsLocked == nil {
		return fmt.Errorf("at least one field must be provided")
	}
	if r.DisplayName != nil {
		v := strings.TrimSpace(*r.DisplayName)
		if v == "" || len([]rune(v)) > 120 {
			return fmt.Errorf("display_name must be non-empty and at most 120 characters")
		}
		r.DisplayName = &v
	}
	if r.ProfileJSON != nil && (len(*r.ProfileJSON) == 0 || !json.Valid(*r.ProfileJSON)) {
		return fmt.Errorf("profile_json must be valid JSON")
	}
	return nil
}
