package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/client"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/scheduler"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

// SidecarCaller abstracts Python Sidecar internal workflow calls for handlers and tests.
type SidecarCaller interface {
	ResumeWorkflow(ctx context.Context, req client.ResumeWorkflowReq, traceID string) (client.ResumeWorkflowResp, error)
	RegenerateFrame(ctx context.Context, frameID uuid.UUID, req client.RegenerateFrameReq, traceID string) (client.FrameVersionResp, error)
}

// JobHandler handles story job creation, status aggregation, HITL gates, and task retries.
type JobHandler struct {
	jobRepo     repository.JobRepository
	sched       *scheduler.Scheduler
	sidecar     SidecarCaller
	usageRepo   repository.UsageRepository
	broker      sse.Broker
	projectRepo repository.ProjectRepository
	assetRepo   repository.AssetRepository
}

// NewJobHandler constructs a JobHandler without sidecar dependencies.
func NewJobHandler(jobRepo repository.JobRepository, sched *scheduler.Scheduler) *JobHandler {
	return &JobHandler{jobRepo: jobRepo, sched: sched}
}

// NewJobHandlerWithSidecar constructs a JobHandler wired to Python Sidecar, UsageRepo, and SSE Broker.
func NewJobHandlerWithSidecar(
	jobRepo repository.JobRepository,
	sched *scheduler.Scheduler,
	sidecar SidecarCaller,
	usageRepo repository.UsageRepository,
	broker sse.Broker,
) *JobHandler {
	return &JobHandler{
		jobRepo:   jobRepo,
		sched:     sched,
		sidecar:   sidecar,
		usageRepo: usageRepo,
		broker:    broker,
	}
}

// SetExportDeps attaches optional project and asset repositories for job listing and export endpoints.
func (h *JobHandler) SetExportDeps(projectRepo repository.ProjectRepository, assetRepo repository.AssetRepository) {
	h.projectRepo = projectRepo
	h.assetRepo = assetRepo
}

// CreateJob handles POST /api/v1/projects/:id/jobs with mandatory X-Idempotency-Key.
func (h *JobHandler) CreateJob(c *gin.Context) {
	projectID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid project id")
		return
	}

	idempotencyKey := strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "X-Idempotency-Key header is required")
		return
	}

	var req model.CreateJobRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	if err := req.NormalizeAndValidate(); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, err.Error())
		return
	}

	threadID := "th_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	traceID := response.GetTraceID(c)
	job, created, err := h.jobRepo.CreateOrGetByIdempotencyKey(
		c.Request.Context(),
		projectID,
		idempotencyKey,
		threadID,
		traceID,
		*req.BudgetLimitUSD,
	)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		response.Fail(c, http.StatusConflict, response.CodeConflict, "idempotency key belongs to a different project")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to create story job")
		return
	}

	if !created {
		response.OK(c, job)
		return
	}

	if h.sched != nil {
		if err := h.sched.EnqueueStoryParseJob(c.Request.Context(), job); err != nil {
			response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to enqueue story job")
			return
		}
		if refreshed, err := h.jobRepo.GetJobByID(c.Request.Context(), job.ID); err == nil {
			job = refreshed
		}
	}

	response.Created(c, job)
}

func (h *JobHandler) buildJobDetail(c *gin.Context, jobID uuid.UUID) (model.JobDetailResponse, error) {
	job, err := h.jobRepo.GetJobByID(c.Request.Context(), jobID)
	if err != nil {
		return model.JobDetailResponse{}, err
	}
	frames, err := h.jobRepo.ListFramesByJob(c.Request.Context(), jobID)
	if err != nil {
		return model.JobDetailResponse{}, err
	}
	versions, err := h.jobRepo.ListVersionsByJob(c.Request.Context(), jobID)
	if err != nil {
		return model.JobDetailResponse{}, err
	}
	tasks, err := h.jobRepo.ListTasksByJob(c.Request.Context(), jobID)
	if err != nil {
		return model.JobDetailResponse{}, err
	}
	return model.JobDetailResponse{
		Job:      job,
		Frames:   frames,
		Versions: versions,
		Tasks:    tasks,
	}, nil
}

// GetJob handles GET /api/v1/jobs/:jobId.
func (h *JobHandler) GetJob(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
		return
	}

	detail, err := h.buildJobDetail(c, jobID)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load job details")
		return
	}
	response.OK(c, detail)
}

// ConfirmGate handles POST /api/v1/jobs/:jobId/gates/:gateNo/confirm (Gate 1 & Gate 2).
func (h *JobHandler) ConfirmGate(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
		return
	}
	gateNo, err := strconv.Atoi(strings.TrimSpace(c.Param("gateNo")))
	if err != nil || (gateNo != 1 && gateNo != 2) {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "gateNo must be 1 or 2")
		return
	}
	if h.sidecar == nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "sidecar client unavailable")
		return
	}

	var req model.ConfirmGateRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}

	job, err := h.jobRepo.GetJobByID(c.Request.Context(), jobID)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load job")
		return
	}

	expectedStage := model.JobStageGate1
	if gateNo == 2 {
		expectedStage = model.JobStageGate2
	}
	if job.Status != model.JobStatusWaitingGate || job.CurrentStage != expectedStage {
		response.Fail(c, http.StatusConflict, response.CodeConflict, "job is not waiting at requested gate")
		return
	}

	traceID := response.GetTraceID(c)
	resumeResp, err := h.sidecar.ResumeWorkflow(c.Request.Context(), client.ResumeWorkflowReq{
		ThreadID:  job.LangGraphThreadID,
		JobID:     job.ID.String(),
		ProjectID: job.ProjectID.String(),
		GateNo:    gateNo,
		UserEdits: req.UserEdits,
	}, traceID)
	if err != nil {
		respondSidecarError(c, err)
		return
	}

	if gateNo == 1 {
		if h.broker != nil {
			_, _ = h.broker.Publish(c.Request.Context(), job.ID.String(), model.EventGateWaiting, map[string]any{
				"job_id":        job.ID.String(),
				"trace_id":      traceID,
				"gate_no":       2,
				"status":        model.JobStatusWaitingGate,
				"current_stage": model.JobStageGate2,
				"frames_count":  len(resumeResp.Frames),
			})
		}
	} else {
		if h.broker != nil {
			_, _ = h.broker.Publish(c.Request.Context(), job.ID.String(), model.EventStageChanged, map[string]any{
				"job_id":        job.ID.String(),
				"trace_id":      traceID,
				"status":        model.JobStatusRunning,
				"current_stage": model.JobStageRenderFrames,
			})
		}

		frames, err := h.jobRepo.ListFramesByJob(c.Request.Context(), jobID)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load frames after gate 2")
			return
		}
		existingTasks, _ := h.jobRepo.ListTasksByJob(c.Request.Context(), jobID)
		hasActiveTask := make(map[uuid.UUID]bool)
		for _, t := range existingTasks {
			if t.TaskType == model.TaskTypeInitialRender && t.Status != model.TaskStatusFailed {
				hasActiveTask[t.FrameID] = true
			}
		}

		for _, frame := range frames {
			if hasActiveTask[frame.ID] {
				continue
			}
			ft, err := h.jobRepo.CreateFrameTask(c.Request.Context(), jobID, frame.ID, model.TaskTypeInitialRender, 2)
			if err != nil {
				response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to create frame task")
				return
			}
			queue := scheduler.QueueDefault
			if frame.FrameIndex == 1 {
				queue = scheduler.QueueCritical
			}
			if h.sched != nil {
				if _, err := h.sched.EnqueueFrameTask(c.Request.Context(), ft.ID, traceID, queue); err != nil {
					response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to enqueue frame task")
					return
				}
			}
		}
	}

	detail, err := h.buildJobDetail(c, jobID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load updated job detail")
		return
	}
	response.OK(c, detail)
}

// RegenerateFrame handles POST /api/v1/frames/:frameId/regenerate (Gate 3).
func (h *JobHandler) RegenerateFrame(c *gin.Context) {
	frameID, err := uuid.Parse(c.Param("frameId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid frame id")
		return
	}
	if h.sidecar == nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "sidecar client unavailable")
		return
	}

	var req model.RegenerateFrameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	req.UserEditNote = strings.TrimSpace(req.UserEditNote)
	if req.UserEditNote == "" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "user_edit_note is required")
		return
	}

	frame, err := h.jobRepo.GetFrameByID(c.Request.Context(), frameID)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "frame not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load frame")
		return
	}

	ctx := c.Request.Context()
	traceID := response.GetTraceID(c)
	task, err := h.jobRepo.CreateFrameTask(ctx, frame.JobID, frame.ID, model.TaskTypeRegenerate, 2)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to create regenerate task")
		return
	}
	if task, err = h.jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusPending, model.TaskStatusQueued, nil, nil); err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to queue regenerate task")
		return
	}
	if task, err = h.jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusQueued, model.TaskStatusRunning, nil, nil); err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to start regenerate task")
		return
	}

	verResp, sidecarErr := h.sidecar.RegenerateFrame(ctx, frameID, client.RegenerateFrameReq{
		JobID:             frame.JobID.String(),
		UserEditNote:      req.UserEditNote,
		CascadeDownstream: req.CascadeDownstream,
		Seed:              req.Seed,
	}, traceID)
	if sidecarErr != nil {
		errCode := "REGEN_ERROR"
		if errors.Is(sidecarErr, scheduler.ErrSafetyBlocked) {
			errCode = "SAFETY_BLOCKED"
		} else if errors.Is(sidecarErr, client.ErrBudgetExceeded) {
			errCode = "BUDGET_EXCEEDED"
		}
		errMsg := sidecarErr.Error()
		_, _ = h.jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusRunning, model.TaskStatusFailed, &errCode, &errMsg)
		if h.broker != nil {
			_, _ = h.broker.Publish(ctx, frame.JobID.String(), model.EventFrameFailed, map[string]any{
				"job_id":     frame.JobID.String(),
				"frame_id":   frame.ID.String(),
				"task_id":    task.ID.String(),
				"trace_id":   traceID,
				"error_code": errCode,
				"message":    errMsg,
			})
		}
		respondSidecarError(c, sidecarErr)
		return
	}

	if task, err = h.jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusRunning, model.TaskStatusVerifying, nil, nil); err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to verify regenerate task")
		return
	}
	if task, err = h.jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusVerifying, model.TaskStatusSucceeded, nil, nil); err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to complete regenerate task")
		return
	}

	verID, _ := uuid.Parse(verResp.ID)
	outVersion := model.FrameVersion{
		ID:             verID,
		FrameID:        frame.ID,
		VersionNo:      verResp.VersionNo,
		UserEditNote:   verResp.UserEditNote,
		PromptSnapshot: verResp.PromptSnapshot,
		ImageURL:       verResp.ImageURL,
		Seed:           verResp.Seed,
		VLMScore:       verResp.VLMScore,
		VLMFeedback:    verResp.VLMFeedback,
	}
	if versions, listErr := h.jobRepo.ListFrameVersions(ctx, frame.ID); listErr == nil {
		for _, v := range versions {
			if v.VersionNo == verResp.VersionNo {
				outVersion = v
				break
			}
		}
	}

	var totalCost float64
	if h.usageRepo != nil {
		if snap, snapErr := h.usageRepo.GetJobBudgetSnapshot(ctx, frame.JobID); snapErr == nil {
			totalCost = snap.TotalCostUSD
		}
	}
	if h.broker != nil {
		_, _ = h.broker.Publish(ctx, frame.JobID.String(), model.EventFrameCompleted, map[string]any{
			"job_id":             frame.JobID.String(),
			"frame_id":           frame.ID.String(),
			"task_id":            task.ID.String(),
			"trace_id":           traceID,
			"frame_index":        frame.FrameIndex,
			"version_no":         outVersion.VersionNo,
			"image_url":          outVersion.ImageURL,
			"job_total_cost_usd": totalCost,
		})
	}

	cascaded := verResp.CascadedFrameIndices
	if cascaded == nil {
		cascaded = []int{}
	}
	response.OK(c, model.RegenerateFrameResponse{
		Task:                 task,
		Version:              outVersion,
		CascadedFrameIndices: cascaded,
	})
}

// SetActiveVersion handles PUT /api/v1/frames/:frameId/active-version.
func (h *JobHandler) SetActiveVersion(c *gin.Context) {
	frameID, err := uuid.Parse(c.Param("frameId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid frame id")
		return
	}

	var req model.SetActiveVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.VersionID == uuid.Nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "valid version_id is required")
		return
	}

	frame, err := h.jobRepo.SetFrameActiveVersion(c.Request.Context(), frameID, req.VersionID)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "frame or version not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to update active frame version")
		return
	}
	response.OK(c, frame)
}

// RetryFrameTask handles POST /api/v1/frame-tasks/:taskId/retry.
func (h *JobHandler) RetryFrameTask(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid task id")
		return
	}
	if h.sched == nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "scheduler unavailable")
		return
	}

	task, err := h.sched.RetryFrameTask(c.Request.Context(), taskID, response.GetTraceID(c))
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "frame task not found")
		return
	}
	if errors.Is(err, repository.ErrInvalidTransition) {
		response.Fail(c, http.StatusConflict, response.CodeConflict, "only FAILED frame tasks can be retried")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to retry frame task")
		return
	}

	response.OK(c, task)
}

func respondSidecarError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, scheduler.ErrSafetyBlocked):
		response.Fail(c, http.StatusUnprocessableEntity, response.CodeSafetyBlocked, "SAFETY_BLOCKED")
	case errors.Is(err, client.ErrBudgetExceeded):
		response.Fail(c, http.StatusPaymentRequired, response.CodeBudgetExceeded, "job budget limit exceeded")
	case errors.Is(err, client.ErrWorkflowConflict):
		response.Fail(c, http.StatusConflict, response.CodeConflict, err.Error())
	case errors.Is(err, repository.ErrNotFound):
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, err.Error())
	default:
		response.Fail(c, http.StatusBadGateway, response.CodeUpstreamError, err.Error())
	}
}

// ListProjectJobs handles GET /api/v1/projects/:id/jobs.
func (h *JobHandler) ListProjectJobs(c *gin.Context) {
	projectID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid project id")
		return
	}
	if h.projectRepo != nil {
		if _, err := h.projectRepo.GetByID(c.Request.Context(), projectID); errors.Is(err, repository.ErrNotFound) {
			response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
			return
		} else if err != nil {
			response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to verify project")
			return
		}
	}
	jobs, err := h.jobRepo.ListJobsByProject(c.Request.Context(), projectID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to list project jobs")
		return
	}
	response.OK(c, gin.H{"items": jobs})
}

// GetJobStates handles GET /api/v1/jobs/:jobId/states (FR-C04 continuity snapshots).
func (h *JobHandler) GetJobStates(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
		return
	}
	if _, err := h.jobRepo.GetJobByID(c.Request.Context(), jobID); errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		return
	} else if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load job")
		return
	}
	states, err := h.jobRepo.ListEpisodicStatesByJob(c.Request.Context(), jobID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to list episodic states")
		return
	}
	response.OK(c, gin.H{"items": states})
}

// ExportJob handles POST /api/v1/jobs/:jobId/export (FR-G01 / FR-G02 storyboard export bundle).
func (h *JobHandler) ExportJob(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
		return
	}
	var req model.ExportJobRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	format := strings.TrimSpace(req.Format)
	if format == "" {
		format = "markdown_json"
	}
	if format != "markdown_json" && format != "long_strip_manifest" {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "format must be markdown_json or long_strip_manifest")
		return
	}

	ctx := c.Request.Context()
	detail, err := h.buildJobDetail(c, jobID)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load job for export")
		return
	}

	projectTitle := "EchoCanvas Storyboard"
	var projectMeta *model.Project
	if h.projectRepo != nil {
		if proj, pErr := h.projectRepo.GetByID(ctx, detail.Job.ProjectID); pErr == nil {
			projectTitle = proj.Title
			projectMeta = &proj
		}
	}
	var assets []model.AssetStyleProfile
	if h.assetRepo != nil {
		if aList, aErr := h.assetRepo.ListByProject(ctx, detail.Job.ProjectID); aErr == nil {
			assets = aList
		}
	}
	states, _ := h.jobRepo.ListEpisodicStatesByJob(ctx, jobID)

	verByID := make(map[uuid.UUID]model.FrameVersion, len(detail.Versions))
	latestByFrame := make(map[uuid.UUID]model.FrameVersion, len(detail.Frames))
	for _, v := range detail.Versions {
		verByID[v.ID] = v
		latestByFrame[v.FrameID] = v
	}

	var md strings.Builder
	md.WriteString(fmt.Sprintf("# %s\n\n", projectTitle))
	md.WriteString(fmt.Sprintf("- **Job ID**: `%s`\n", detail.Job.ID))
	md.WriteString(fmt.Sprintf("- **Trace ID**: `%s`\n", detail.Job.TraceID))
	md.WriteString(fmt.Sprintf("- **Stage / Status**: `%s / %s`\n", detail.Job.CurrentStage, detail.Job.Status))
	md.WriteString(fmt.Sprintf("- **Total Cost (USD)**: `$%.6f`\n\n", detail.Job.TotalCostUSD))

	if len(assets) > 0 {
		md.WriteString("## Locked & Extracted Assets\n\n")
		for _, a := range assets {
			md.WriteString(fmt.Sprintf("- **%s** (`%s`, `%s`, source=`%s`)\n", a.DisplayName, a.AssetType, a.EntityKey, a.Source))
		}
		md.WriteString("\n")
	}

	exportFrames := make([]map[string]any, 0, len(detail.Frames))
	md.WriteString("## Storyboard Frames\n\n")
	for _, f := range detail.Frames {
		var active *model.FrameVersion
		if f.ActiveVersionID != nil {
			if found, ok := verByID[*f.ActiveVersionID]; ok {
				active = &found
			}
		}
		if active == nil {
			if latest, ok := latestByFrame[f.ID]; ok {
				active = &latest
			}
		}

		var scriptMap map[string]any
		_ = json.Unmarshal(f.ScriptJSON, &scriptMap)

		entry := map[string]any{
			"frame_id":    f.ID,
			"frame_index": f.FrameIndex,
			"script":      scriptMap,
		}
		md.WriteString(fmt.Sprintf("### Frame %02d\n\n", f.FrameIndex))
		if scriptMap != nil {
			md.WriteString(fmt.Sprintf("- **Shot / Angle**: %v / %v\n", scriptMap["shot_type"], scriptMap["camera_angle"]))
			md.WriteString(fmt.Sprintf("- **Action**: %v\n", scriptMap["action"]))
			if d, ok := scriptMap["dialogue"]; ok && fmt.Sprintf("%v", d) != "" {
				md.WriteString(fmt.Sprintf("- **Dialogue**: %v\n", d))
			}
		}
		if active != nil {
			entry["active_version"] = active
			md.WriteString(fmt.Sprintf("- **Active Version**: v%d\n", active.VersionNo))
			md.WriteString(fmt.Sprintf("- **Image URL**: %s\n", active.ImageURL))
			if active.VLMScore != nil {
				md.WriteString(fmt.Sprintf("- **VLM QA Score**: %.2f\n", *active.VLMScore))
			}
		}
		md.WriteString("\n")
		exportFrames = append(exportFrames, entry)
	}

	manifest := map[string]any{
		"project":         projectMeta,
		"job":             detail.Job,
		"assets":          assets,
		"frames":          exportFrames,
		"episodic_states": states,
		"total_cost_usd":  detail.Job.TotalCostUSD,
	}

	response.OK(c, model.ExportJobResponse{
		JobID:        detail.Job.ID,
		ProjectTitle: projectTitle,
		Format:       format,
		Filename:     fmt.Sprintf("echocanvas_%s_export.json", detail.Job.ID.String()[:8]),
		Markdown:     md.String(),
		Manifest:     manifest,
		TotalCostUSD: detail.Job.TotalCostUSD,
	})
}
