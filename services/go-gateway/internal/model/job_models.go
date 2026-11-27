package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	JobStatusPending       = "PENDING"
	JobStatusRunning       = "RUNNING"
	JobStatusWaitingGate   = "WAITING_GATE"
	JobStatusSucceeded     = "SUCCEEDED"
	JobStatusPartialFailed = "PARTIAL_FAILED"
	JobStatusFailed        = "FAILED"
	JobStatusPausedBudget  = "PAUSED_BUDGET"
)

const (
	JobStageInit          = "INIT"
	JobStageExtractAssets = "EXTRACT_ASSETS"
	JobStageGate1         = "GATE_1"
	JobStagePlanScript    = "PLAN_SCRIPT"
	JobStageGate2         = "GATE_2"
	JobStageRenderFrames  = "RENDER_FRAMES"
	JobStageDone          = "DONE"
)

const (
	TaskStatusPending   = "PENDING"
	TaskStatusQueued    = "QUEUED"
	TaskStatusRunning   = "RUNNING"
	TaskStatusVerifying = "VERIFYING"
	TaskStatusSucceeded = "SUCCEEDED"
	TaskStatusFailed    = "FAILED"
)

const (
	TaskTypeInitialRender = "INITIAL_RENDER"
	TaskTypeRegenerate    = "REGENERATE"
)

const (
	EventStageChanged   = "stage_changed"
	EventGateWaiting    = "gate_waiting"
	EventFrameProgress  = "frame_progress"
	EventFrameCompleted = "frame_completed"
	EventFrameFailed    = "frame_failed"
	EventCostUpdated    = "cost_updated"
	EventJobCompleted   = "job_completed"
	EventHeartbeat      = "heartbeat"
)

// StoryJob maps to story_jobs.
type StoryJob struct {
	ID                uuid.UUID `json:"id"`
	ProjectID         uuid.UUID `json:"project_id"`
	IdempotencyKey    string    `json:"idempotency_key"`
	LangGraphThreadID string    `json:"langgraph_thread_id"`
	TraceID           string    `json:"trace_id"`
	Status            string    `json:"status"`
	CurrentStage      string    `json:"current_stage"`
	BudgetLimitUSD    float64   `json:"budget_limit_usd"`
	TotalCostUSD      float64   `json:"total_cost_usd"`
	ErrorMessage      *string   `json:"error_message,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// StoryFrame maps to story_frames.
type StoryFrame struct {
	ID              uuid.UUID       `json:"id"`
	JobID           uuid.UUID       `json:"job_id"`
	FrameIndex      int             `json:"frame_index"`
	ScriptJSON      json.RawMessage `json:"script_json"`
	ActiveVersionID *uuid.UUID      `json:"active_version_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// FrameVersion maps to frame_versions.
type FrameVersion struct {
	ID             uuid.UUID       `json:"id"`
	FrameID        uuid.UUID       `json:"frame_id"`
	VersionNo      int             `json:"version_no"`
	UserEditNote   *string         `json:"user_edit_note,omitempty"`
	PromptSnapshot string          `json:"prompt_snapshot"`
	ImageURL       string          `json:"image_url"`
	Seed           *int64          `json:"seed,omitempty"`
	VLMScore       *float64        `json:"vlm_score,omitempty"`
	VLMFeedback    json.RawMessage `json:"vlm_feedback,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// FrameTask maps to frame_tasks.
type FrameTask struct {
	ID          uuid.UUID  `json:"id"`
	JobID       uuid.UUID  `json:"job_id"`
	FrameID     uuid.UUID  `json:"frame_id"`
	TaskType    string     `json:"task_type"`
	Status      string     `json:"status"`
	RetryCount  int        `json:"retry_count"`
	MaxRetries  int        `json:"max_retries"`
	ErrorCode   *string    `json:"error_code,omitempty"`
	ErrorDetail *string    `json:"error_detail,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CreateJobRequest defines payload for POST /api/v1/projects/:id/jobs.
type CreateJobRequest struct {
	BudgetLimitUSD *float64 `json:"budget_limit_usd"`
}

// NormalizeAndValidate applies defaults and validates budget bounds.
func (r *CreateJobRequest) NormalizeAndValidate() error {
	if r.BudgetLimitUSD == nil {
		v := 0.50
		r.BudgetLimitUSD = &v
	}
	if *r.BudgetLimitUSD <= 0 || *r.BudgetLimitUSD > 100.0 {
		return fmt.Errorf("budget_limit_usd must be > 0 and <= 100")
	}
	return nil
}

// JobDetailResponse aggregates a job with its frames, versions, and tasks.
type JobDetailResponse struct {
	Job      StoryJob       `json:"job"`
	Frames   []StoryFrame   `json:"frames"`
	Versions []FrameVersion `json:"versions"`
	Tasks    []FrameTask    `json:"tasks"`
}

// SSEEvent represents a streamed event stored in Redis Streams and fanned out via Pub/Sub.
type SSEEvent struct {
	ID        string          `json:"id"`
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// ConfirmGateRequest defines payload for POST /api/v1/jobs/:jobId/gates/:gateNo/confirm.
type ConfirmGateRequest struct {
	UserEdits map[string]any `json:"user_edits,omitempty"`
}

// RegenerateFrameRequest defines payload for POST /api/v1/frames/:frameId/regenerate.
type RegenerateFrameRequest struct {
	UserEditNote      string `json:"user_edit_note"`
	CascadeDownstream bool   `json:"cascade_downstream,omitempty"`
	Seed              *int64 `json:"seed,omitempty"`
}

// SetActiveVersionRequest defines payload for PUT /api/v1/frames/:frameId/active-version.
type SetActiveVersionRequest struct {
	VersionID uuid.UUID `json:"version_id"`
}

// RegenerateFrameResponse wraps task, created version, and cascaded downstream frame indices.
type RegenerateFrameResponse struct {
	Task                 FrameTask    `json:"task"`
	Version              FrameVersion `json:"version"`
	CascadedFrameIndices []int        `json:"cascaded_frame_indices"`
}

// EpisodicEntityState maps to episodic_entity_states.
type EpisodicEntityState struct {
	ID           uuid.UUID       `json:"id"`
	JobID        uuid.UUID       `json:"job_id"`
	FrameIndex   int             `json:"frame_index"`
	EntityKey    string          `json:"entity_key"`
	StateJSON    json.RawMessage `json:"state_json"`
	DiffFromPrev json.RawMessage `json:"diff_from_prev,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// ExportJobRequest defines payload for POST /api/v1/jobs/:jobId/export.
type ExportJobRequest struct {
	Format string `json:"format,omitempty"`
}

// ExportJobResponse returns structured script Markdown and manifest JSON for FR-G01/FR-G02.
type ExportJobResponse struct {
	JobID        uuid.UUID      `json:"job_id"`
	ProjectTitle string         `json:"project_title"`
	Format       string         `json:"format"`
	Filename     string         `json:"filename"`
	Markdown     string         `json:"markdown"`
	Manifest     map[string]any `json:"manifest"`
	TotalCostUSD float64        `json:"total_cost_usd"`
}
