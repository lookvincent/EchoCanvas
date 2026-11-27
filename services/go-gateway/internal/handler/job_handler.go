package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/scheduler"
)

// JobHandler handles story job creation, status aggregation, and task retries.
type JobHandler struct {
	jobRepo repository.JobRepository
	sched   *scheduler.Scheduler
}

// NewJobHandler constructs a JobHandler.
func NewJobHandler(jobRepo repository.JobRepository, sched *scheduler.Scheduler) *JobHandler {
	return &JobHandler{jobRepo: jobRepo, sched: sched}
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

// GetJob handles GET /api/v1/jobs/:jobId.
func (h *JobHandler) GetJob(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
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

	frames, err := h.jobRepo.ListFramesByJob(c.Request.Context(), jobID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load frames")
		return
	}
	versions, err := h.jobRepo.ListVersionsByJob(c.Request.Context(), jobID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load frame versions")
		return
	}
	tasks, err := h.jobRepo.ListTasksByJob(c.Request.Context(), jobID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load frame tasks")
		return
	}

	response.OK(c, model.JobDetailResponse{
		Job:      job,
		Frames:   frames,
		Versions: versions,
		Tasks:    tasks,
	})
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
