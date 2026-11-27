package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/scheduler"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

var (
	// ErrBudgetExceeded indicates the Python Sidecar received HTTP 402 from Go AIGC Gateway.
	ErrBudgetExceeded = errors.New("job budget exceeded")
	// ErrWorkflowConflict indicates an invalid Gate transition in LangGraph.
	ErrWorkflowConflict = errors.New("workflow gate state conflict")
)

// HTTPStatusError captures unexpected HTTP error responses from Python Sidecar.
type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("py-agent HTTP %d: %s", e.StatusCode, e.Body)
}

// ParseStoryReq maps to POST /internal/v1/workflows/parse-story.
type ParseStoryReq struct {
	JobID        string `json:"job_id"`
	ProjectID    string `json:"project_id,omitempty"`
	ThreadID     string `json:"thread_id"`
	RawStory     string `json:"raw_story"`
	StylePreset  string `json:"style_preset"`
	AspectRatio  string `json:"aspect_ratio"`
	TargetFrames int    `json:"target_frames"`
}

// ParseStoryResp captures Gate 1 asset extraction output.
type ParseStoryResp struct {
	JobID        string            `json:"job_id"`
	ThreadID     string            `json:"thread_id"`
	Status       string            `json:"status"`
	CurrentStage string            `json:"current_stage"`
	Assets       []json.RawMessage `json:"assets"`
	TraceID      string            `json:"trace_id"`
}

// ResumeWorkflowReq maps to POST /internal/v1/workflows/resume.
type ResumeWorkflowReq struct {
	ThreadID  string         `json:"thread_id"`
	JobID     string         `json:"job_id,omitempty"`
	ProjectID string         `json:"project_id,omitempty"`
	GateNo    int            `json:"gate_no"`
	UserEdits map[string]any `json:"user_edits,omitempty"`
}

// ResumeWorkflowResp captures Gate 1/2 resume response from Python Sidecar.
type ResumeWorkflowResp struct {
	JobID        string            `json:"job_id,omitempty"`
	ThreadID     string            `json:"thread_id"`
	Status       string            `json:"status"`
	CurrentStage string            `json:"current_stage"`
	Frames       []json.RawMessage `json:"frames"`
	TraceID      string            `json:"trace_id"`
}

// RenderFrameReq maps to POST /internal/v1/frames/{frameId}/render.
type RenderFrameReq struct {
	JobID       string `json:"job_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Seed        *int64 `json:"seed,omitempty"`
}

// RegenerateFrameReq maps to POST /internal/v1/frames/{frameId}/regenerate.
type RegenerateFrameReq struct {
	JobID             string `json:"job_id,omitempty"`
	UserEditNote      string `json:"user_edit_note"`
	CascadeDownstream bool   `json:"cascade_downstream,omitempty"`
	Seed              *int64 `json:"seed,omitempty"`
}

// FrameVersionResp captures generated/regenerated frame_versions details from Python Sidecar.
type FrameVersionResp struct {
	ID                   string          `json:"id,omitempty"`
	FrameID              string          `json:"frame_id"`
	VersionNo            int             `json:"version_no"`
	UserEditNote         *string         `json:"user_edit_note,omitempty"`
	PromptSnapshot       string          `json:"prompt_snapshot"`
	ImageURL             string          `json:"image_url"`
	Seed                 *int64          `json:"seed,omitempty"`
	VLMScore             *float64        `json:"vlm_score,omitempty"`
	VLMFeedback          json.RawMessage `json:"vlm_feedback,omitempty"`
	CascadedFrameIndices []int           `json:"cascaded_frame_indices"`
}

// PySidecarClient calls Python Sidecar /internal/v1/* endpoints over HTTP/JSON.
type PySidecarClient struct {
	baseURL       string
	internalToken string
	httpClient    *http.Client
}

// NewPySidecarClient constructs a PySidecarClient with trace & auth injection.
func NewPySidecarClient(baseURL, internalToken string, httpClient *http.Client) *PySidecarClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &PySidecarClient{
		baseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		internalToken: strings.TrimSpace(internalToken),
		httpClient:    httpClient,
	}
}

func (c *PySidecarClient) postJSON(ctx context.Context, path, traceID string, reqBody, respOut any) error {
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.internalToken != "" {
		req.Header.Set("X-Internal-Token", c.internalToken)
	}
	if traceID != "" {
		req.Header.Set("X-Trace-ID", traceID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		bodyStr := strings.TrimSpace(string(bodyBytes))
		switch resp.StatusCode {
		case http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %s", scheduler.ErrSafetyBlocked, bodyStr)
		case http.StatusPaymentRequired:
			return fmt.Errorf("%w: %s", ErrBudgetExceeded, bodyStr)
		case http.StatusConflict:
			return fmt.Errorf("%w: %s", ErrWorkflowConflict, bodyStr)
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", repository.ErrNotFound, bodyStr)
		default:
			return &HTTPStatusError{StatusCode: resp.StatusCode, Body: bodyStr}
		}
	}

	if respOut != nil && len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, respOut); err != nil {
			return fmt.Errorf("decode py-agent response: %w", err)
		}
	}
	return nil
}

// ParseStory calls POST /internal/v1/workflows/parse-story.
func (c *PySidecarClient) ParseStory(ctx context.Context, req ParseStoryReq, traceID string) (ParseStoryResp, error) {
	var out ParseStoryResp
	err := c.postJSON(ctx, "/internal/v1/workflows/parse-story", traceID, req, &out)
	return out, err
}

// ResumeWorkflow calls POST /internal/v1/workflows/resume.
func (c *PySidecarClient) ResumeWorkflow(ctx context.Context, req ResumeWorkflowReq, traceID string) (ResumeWorkflowResp, error) {
	var out ResumeWorkflowResp
	err := c.postJSON(ctx, "/internal/v1/workflows/resume", traceID, req, &out)
	return out, err
}

// RenderFrame calls POST /internal/v1/frames/{frameID}/render.
func (c *PySidecarClient) RenderFrame(ctx context.Context, frameID uuid.UUID, req RenderFrameReq, traceID string) (FrameVersionResp, error) {
	var out FrameVersionResp
	err := c.postJSON(ctx, fmt.Sprintf("/internal/v1/frames/%s/render", frameID), traceID, req, &out)
	return out, err
}

// RegenerateFrame calls POST /internal/v1/frames/{frameID}/regenerate.
func (c *PySidecarClient) RegenerateFrame(ctx context.Context, frameID uuid.UUID, req RegenerateFrameReq, traceID string) (FrameVersionResp, error) {
	var out FrameVersionResp
	err := c.postJSON(ctx, fmt.Sprintf("/internal/v1/frames/%s/regenerate", frameID), traceID, req, &out)
	return out, err
}

// SidecarWorkerAdapter bridges Scheduler interfaces (StoryParser & FrameExecutor) to PySidecarClient.
type SidecarWorkerAdapter struct {
	client      *PySidecarClient
	projectRepo repository.ProjectRepository
	jobRepo     repository.JobRepository
	usageRepo   repository.UsageRepository
	broker      sse.Broker
}

// NewSidecarWorkerAdapter builds a worker adapter for Go Asynq Scheduler.
func NewSidecarWorkerAdapter(
	client *PySidecarClient,
	projectRepo repository.ProjectRepository,
	jobRepo repository.JobRepository,
	usageRepo repository.UsageRepository,
	broker sse.Broker,
) *SidecarWorkerAdapter {
	return &SidecarWorkerAdapter{
		client:      client,
		projectRepo: projectRepo,
		jobRepo:     jobRepo,
		usageRepo:   usageRepo,
		broker:      broker,
	}
}

// ParseStory loads job/project metadata, invokes Python Gate 1 extraction, and emits gate_waiting SSE.
func (a *SidecarWorkerAdapter) ParseStory(ctx context.Context, payload scheduler.StoryParsePayload) error {
	jobID, err := uuid.Parse(payload.JobID)
	if err != nil {
		return fmt.Errorf("invalid job id: %w", err)
	}
	projectID, err := uuid.Parse(payload.ProjectID)
	if err != nil {
		return fmt.Errorf("invalid project id: %w", err)
	}
	job, err := a.jobRepo.GetJobByID(ctx, jobID)
	if err != nil {
		return err
	}
	proj, err := a.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return err
	}

	resp, err := a.client.ParseStory(ctx, ParseStoryReq{
		JobID:        job.ID.String(),
		ProjectID:    proj.ID.String(),
		ThreadID:     job.LangGraphThreadID,
		RawStory:     proj.RawStory,
		StylePreset:  proj.StylePreset,
		AspectRatio:  proj.AspectRatio,
		TargetFrames: proj.TargetFrames,
	}, payload.TraceID)
	if err != nil {
		return err
	}

	if a.broker != nil {
		_, _ = a.broker.Publish(ctx, job.ID.String(), model.EventGateWaiting, map[string]any{
			"job_id":        job.ID.String(),
			"trace_id":      payload.TraceID,
			"gate_no":       1,
			"status":        resp.Status,
			"current_stage": resp.CurrentStage,
			"assets_count":  len(resp.Assets),
		})
	}
	return nil
}

// ExecuteFrame triggers single-frame context assembly and image generation in Python Sidecar.
func (a *SidecarWorkerAdapter) ExecuteFrame(ctx context.Context, task model.FrameTask, traceID string) (scheduler.FrameExecutionResult, error) {
	ver, err := a.client.RenderFrame(ctx, task.FrameID, RenderFrameReq{
		JobID: task.JobID.String(),
	}, traceID)
	if err != nil {
		return scheduler.FrameExecutionResult{}, err
	}

	var totalCost float64
	if a.usageRepo != nil {
		if snap, snapErr := a.usageRepo.GetJobBudgetSnapshot(ctx, task.JobID); snapErr == nil {
			totalCost = snap.TotalCostUSD
		}
	}

	score := 0.0
	if ver.VLMScore != nil {
		score = *ver.VLMScore
	}
	return scheduler.FrameExecutionResult{
		VersionNo:    ver.VersionNo,
		ImageURL:     ver.ImageURL,
		VLMScore:     score,
		TotalCostUSD: totalCost,
	}, nil
}
