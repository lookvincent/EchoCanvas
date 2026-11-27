package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

const (
	QueueCritical = "critical"
	QueueDefault  = "default"

	TypeStoryParse  = "story:parse"
	TypeFrameRender = "frame:render"
)

// ErrSafetyBlocked signals that upstream moderation rejected generation; retries must halt immediately.
var ErrSafetyBlocked = errors.New("safety policy blocked")

// StoryParsePayload carries parameters for initial narrative asset extraction.
type StoryParsePayload struct {
	JobID     string `json:"job_id"`
	ProjectID string `json:"project_id"`
	TraceID   string `json:"trace_id"`
}

// FrameRenderPayload carries parameters for rendering/verifying a single frame task.
type FrameRenderPayload struct {
	TaskID  string `json:"task_id"`
	JobID   string `json:"job_id"`
	FrameID string `json:"frame_id"`
	TraceID string `json:"trace_id"`
}

// FrameExecutionResult captures outcomes emitted by a frame renderer.
type FrameExecutionResult struct {
	VersionNo    int
	ImageURL     string
	VLMScore     float64
	StepCostUSD  float64
	TotalCostUSD float64
}

// FrameExecutor executes rendering + VLM verification for a single FrameTask.
type FrameExecutor interface {
	ExecuteFrame(ctx context.Context, task model.FrameTask, traceID string) (FrameExecutionResult, error)
}

// StoryParser triggers initial story parsing for a StoryJob.
type StoryParser interface {
	ParseStory(ctx context.Context, payload StoryParsePayload) error
}

// Enqueuer abstracts Asynq task submission for testability.
type Enqueuer interface {
	EnqueueStoryParse(ctx context.Context, payload StoryParsePayload) error
	EnqueueFrameRender(ctx context.Context, payload FrameRenderPayload, queue string, delay time.Duration) error
}

// AsynqEnqueuer implements Enqueuer using *asynq.Client.
type AsynqEnqueuer struct {
	client *asynq.Client
}

// NewAsynqEnqueuer creates an Asynq-backed Enqueuer.
func NewAsynqEnqueuer(client *asynq.Client) *AsynqEnqueuer {
	return &AsynqEnqueuer{client: client}
}

func (e *AsynqEnqueuer) EnqueueStoryParse(ctx context.Context, payload StoryParsePayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task := asynq.NewTask(TypeStoryParse, raw)
	_, err = e.client.EnqueueContext(ctx, task, asynq.Queue(QueueCritical), asynq.MaxRetry(0))
	return err
}

func (e *AsynqEnqueuer) EnqueueFrameRender(ctx context.Context, payload FrameRenderPayload, queue string, delay time.Duration) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if queue == "" {
		queue = QueueDefault
	}
	task := asynq.NewTask(TypeFrameRender, raw)
	opts := []asynq.Option{asynq.Queue(queue), asynq.MaxRetry(0)}
	if delay > 0 {
		opts = append(opts, asynq.ProcessIn(delay))
	}
	_, err = e.client.EnqueueContext(ctx, task, opts...)
	return err
}

// RetryDelay implements exponential backoff: 2s -> 6s -> 18s (TDD section 5.1).
func RetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 2 * time.Second
	}
	if attempt == 2 {
		return 6 * time.Second
	}
	return 18 * time.Second
}

// Scheduler coordinates StoryJob and FrameTask async lifecycles with semaphore concurrency limits.
type Scheduler struct {
	repo     repository.JobRepository
	enqueuer Enqueuer
	broker   sse.Broker
	executor FrameExecutor
	parser   StoryParser
	sem      chan struct{}
	logger   *slog.Logger
}

// New constructs a Scheduler with bounded vision concurrency semaphore (default 3).
func New(
	repo repository.JobRepository,
	enqueuer Enqueuer,
	broker sse.Broker,
	executor FrameExecutor,
	parser StoryParser,
	maxConcurrentVision int,
	logger *slog.Logger,
) *Scheduler {
	if maxConcurrentVision <= 0 {
		maxConcurrentVision = 3
	}
	return &Scheduler{
		repo:     repo,
		enqueuer: enqueuer,
		broker:   broker,
		executor: executor,
		parser:   parser,
		sem:      make(chan struct{}, maxConcurrentVision),
		logger:   logger,
	}
}

// EnqueueStoryParseJob transitions a newly created job to RUNNING/EXTRACT_ASSETS and enqueues it.
func (s *Scheduler) EnqueueStoryParseJob(ctx context.Context, job model.StoryJob) error {
	if err := s.repo.UpdateJobStatus(ctx, job.ID, model.JobStatusRunning, model.JobStageExtractAssets, nil); err != nil {
		return err
	}
	if s.enqueuer != nil {
		if err := s.enqueuer.EnqueueStoryParse(ctx, StoryParsePayload{
			JobID:     job.ID.String(),
			ProjectID: job.ProjectID.String(),
			TraceID:   job.TraceID,
		}); err != nil {
			return err
		}
	}
	if s.broker != nil {
		_, _ = s.broker.Publish(ctx, job.ID.String(), model.EventStageChanged, map[string]any{
			"job_id":        job.ID.String(),
			"trace_id":      job.TraceID,
			"status":        model.JobStatusRunning,
			"current_stage": model.JobStageExtractAssets,
		})
	}
	return nil
}

// EnqueueFrameTask transitions PENDING -> QUEUED and pushes onto the specified queue.
func (s *Scheduler) EnqueueFrameTask(ctx context.Context, taskID uuid.UUID, traceID, queue string) (model.FrameTask, error) {
	t, err := s.repo.TransitionTaskStatus(ctx, taskID, model.TaskStatusPending, model.TaskStatusQueued, nil, nil)
	if err != nil {
		return model.FrameTask{}, err
	}
	if s.enqueuer != nil {
		if err := s.enqueuer.EnqueueFrameRender(ctx, FrameRenderPayload{
			TaskID:  t.ID.String(),
			JobID:   t.JobID.String(),
			FrameID: t.FrameID.String(),
			TraceID: traceID,
		}, queue, 0); err != nil {
			return model.FrameTask{}, err
		}
	}
	if s.broker != nil {
		_, _ = s.broker.Publish(ctx, t.JobID.String(), model.EventFrameProgress, map[string]any{
			"job_id":   t.JobID.String(),
			"frame_id": t.FrameID.String(),
			"task_id":  t.ID.String(),
			"status":   t.Status,
			"trace_id": traceID,
		})
	}
	return t, nil
}

// RetryFrameTask manually resets a FAILED task to QUEUED and enqueues on the critical queue.
func (s *Scheduler) RetryFrameTask(ctx context.Context, taskID uuid.UUID, traceID string) (model.FrameTask, error) {
	t, err := s.repo.TransitionTaskStatus(ctx, taskID, model.TaskStatusFailed, model.TaskStatusQueued, nil, nil)
	if err != nil {
		return model.FrameTask{}, err
	}
	if s.enqueuer != nil {
		if err := s.enqueuer.EnqueueFrameRender(ctx, FrameRenderPayload{
			TaskID:  t.ID.String(),
			JobID:   t.JobID.String(),
			FrameID: t.FrameID.String(),
			TraceID: traceID,
		}, QueueCritical, 0); err != nil {
			return model.FrameTask{}, err
		}
	}
	if s.broker != nil {
		_, _ = s.broker.Publish(ctx, t.JobID.String(), model.EventFrameProgress, map[string]any{
			"job_id":   t.JobID.String(),
			"frame_id": t.FrameID.String(),
			"task_id":  t.ID.String(),
			"status":   t.Status,
			"trace_id": traceID,
		})
	}
	return t, nil
}

// ProcessStoryParse handles a story:parse task from Asynq.
func (s *Scheduler) ProcessStoryParse(ctx context.Context, payload StoryParsePayload) error {
	if s.parser == nil {
		return nil
	}
	return s.parser.ParseStory(ctx, payload)
}

// ProcessFrameTask executes a queued frame task under concurrency semaphore and state-machine guards.
func (s *Scheduler) ProcessFrameTask(ctx context.Context, payload FrameRenderPayload) error {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	taskID, err := uuid.Parse(payload.TaskID)
	if err != nil {
		return fmt.Errorf("invalid task id %q: %w", payload.TaskID, err)
	}

	task, err := s.repo.TransitionTaskStatus(ctx, taskID, model.TaskStatusQueued, model.TaskStatusRunning, nil, nil)
	if errors.Is(err, repository.ErrInvalidTransition) || errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	if s.broker != nil {
		_, _ = s.broker.Publish(ctx, task.JobID.String(), model.EventFrameProgress, map[string]any{
			"job_id":   task.JobID.String(),
			"frame_id": task.FrameID.String(),
			"task_id":  task.ID.String(),
			"status":   model.TaskStatusRunning,
			"trace_id": payload.TraceID,
		})
	}

	if s.executor == nil {
		return fmt.Errorf("frame executor is not configured")
	}

	res, execErr := s.executor.ExecuteFrame(ctx, task, payload.TraceID)
	if execErr == nil {
		if _, err := s.repo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusRunning, model.TaskStatusVerifying, nil, nil); err != nil {
			return err
		}
		if _, err := s.repo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusVerifying, model.TaskStatusSucceeded, nil, nil); err != nil {
			return err
		}
		if s.broker != nil {
			_, _ = s.broker.Publish(ctx, task.JobID.String(), model.EventFrameCompleted, map[string]any{
				"job_id":             task.JobID.String(),
				"frame_id":           task.FrameID.String(),
				"task_id":            task.ID.String(),
				"trace_id":           payload.TraceID,
				"version_no":         res.VersionNo,
				"image_url":          res.ImageURL,
				"vlm_score":          res.VLMScore,
				"step_cost_usd":      res.StepCostUSD,
				"job_total_cost_usd": res.TotalCostUSD,
			})
		}
		return nil
	}

	errDetail := execErr.Error()
	if errors.Is(execErr, ErrSafetyBlocked) {
		errCode := "SAFETY_BLOCKED"
		_, transErr := s.repo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusRunning, model.TaskStatusFailed, &errCode, &errDetail)
		if s.broker != nil {
			_, _ = s.broker.Publish(ctx, task.JobID.String(), model.EventFrameFailed, map[string]any{
				"job_id":     task.JobID.String(),
				"frame_id":   task.FrameID.String(),
				"task_id":    task.ID.String(),
				"trace_id":   payload.TraceID,
				"error_code": errCode,
				"message":    errDetail,
			})
		}
		return transErr
	}

	if task.RetryCount < task.MaxRetries {
		errCode := "TRANSIENT_ERROR"
		updated, incErr := s.repo.IncrementTaskRetry(ctx, task.ID, model.TaskStatusQueued, &errCode, &errDetail)
		if incErr != nil {
			return incErr
		}
		if s.enqueuer != nil {
			delay := RetryDelay(updated.RetryCount)
			return s.enqueuer.EnqueueFrameRender(ctx, payload, QueueDefault, delay)
		}
		return nil
	}

	errCode := "MAX_RETRIES_EXCEEDED"
	if _, err := s.repo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusRunning, model.TaskStatusFailed, &errCode, &errDetail); err != nil {
		return err
	}
	if s.broker != nil {
		_, _ = s.broker.Publish(ctx, task.JobID.String(), model.EventFrameFailed, map[string]any{
			"job_id":     task.JobID.String(),
			"frame_id":   task.FrameID.String(),
			"task_id":    task.ID.String(),
			"trace_id":   payload.TraceID,
			"error_code": errCode,
			"message":    errDetail,
		})
	}
	return nil
}

// NewAsynqServer configures the Redis-backed Asynq worker server with priority queues.
func NewAsynqServer(redisAddr string, sched *Scheduler) (*asynq.Server, *asynq.ServeMux) {
	srv := asynq.NewServer(
		asynq.RedisClientOpt{Addr: redisAddr},
		asynq.Config{
			Concurrency: 10,
			Queues: map[string]int{
				QueueCritical: 6,
				QueueDefault:  3,
			},
		},
	)
	mux := asynq.NewServeMux()
	mux.HandleFunc(TypeStoryParse, func(ctx context.Context, t *asynq.Task) error {
		var p StoryParsePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		return sched.ProcessStoryParse(ctx, p)
	})
	mux.HandleFunc(TypeFrameRender, func(ctx context.Context, t *asynq.Task) error {
		var p FrameRenderPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		return sched.ProcessFrameTask(ctx, p)
	})
	return srv, mux
}
