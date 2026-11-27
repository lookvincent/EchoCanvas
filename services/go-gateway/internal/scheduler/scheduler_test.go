package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
	"github.com/redis/go-redis/v9"
)

type memJobRepo struct {
	mu       sync.Mutex
	jobs     map[uuid.UUID]model.StoryJob
	idemIdx  map[string]uuid.UUID
	frames   map[uuid.UUID]model.StoryFrame
	versions map[uuid.UUID]model.FrameVersion
	tasks    map[uuid.UUID]model.FrameTask
}

func newMemJobRepo() *memJobRepo {
	return &memJobRepo{
		jobs:     make(map[uuid.UUID]model.StoryJob),
		idemIdx:  make(map[string]uuid.UUID),
		frames:   make(map[uuid.UUID]model.StoryFrame),
		versions: make(map[uuid.UUID]model.FrameVersion),
		tasks:    make(map[uuid.UUID]model.FrameTask),
	}
}

func (r *memJobRepo) CreateOrGetByIdempotencyKey(_ context.Context, projectID uuid.UUID, key, threadID, traceID string, budget float64) (model.StoryJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.idemIdx[key]; ok {
		j := r.jobs[id]
		if j.ProjectID != projectID {
			return model.StoryJob{}, false, repository.ErrConflict
		}
		return j, false, nil
	}
	now := time.Now().UTC()
	j := model.StoryJob{
		ID:                uuid.New(),
		ProjectID:         projectID,
		IdempotencyKey:    key,
		LangGraphThreadID: threadID,
		TraceID:           traceID,
		Status:            model.JobStatusPending,
		CurrentStage:      model.JobStageInit,
		BudgetLimitUSD:    budget,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	r.jobs[j.ID] = j
	r.idemIdx[key] = j.ID
	return j, true, nil
}

func (r *memJobRepo) GetJobByID(_ context.Context, jobID uuid.UUID) (model.StoryJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return model.StoryJob{}, repository.ErrNotFound
	}
	return j, nil
}

func (r *memJobRepo) UpdateJobStatus(_ context.Context, jobID uuid.UUID, status, stage string, errMsg *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return repository.ErrNotFound
	}
	j.Status = status
	j.CurrentStage = stage
	if errMsg != nil {
		j.ErrorMessage = errMsg
	}
	r.jobs[jobID] = j
	return nil
}

func (r *memJobRepo) CreateFrame(_ context.Context, jobID uuid.UUID, idx int, script json.RawMessage) (model.StoryFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	f := model.StoryFrame{ID: uuid.New(), JobID: jobID, FrameIndex: idx, ScriptJSON: script, CreatedAt: now, UpdatedAt: now}
	r.frames[f.ID] = f
	return f, nil
}

func (r *memJobRepo) CreateFrameTask(_ context.Context, jobID, frameID uuid.UUID, taskType string, maxRetries int) (model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := model.FrameTask{
		ID:         uuid.New(),
		JobID:      jobID,
		FrameID:    frameID,
		TaskType:   taskType,
		Status:     model.TaskStatusPending,
		MaxRetries: maxRetries,
		CreatedAt:  time.Now().UTC(),
	}
	r.tasks[t.ID] = t
	return t, nil
}

func (r *memJobRepo) GetFrameTaskByID(_ context.Context, taskID uuid.UUID) (model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return model.FrameTask{}, repository.ErrNotFound
	}
	return t, nil
}

func (r *memJobRepo) ListFramesByJob(_ context.Context, jobID uuid.UUID) ([]model.StoryFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.StoryFrame, 0)
	for _, f := range r.frames {
		if f.JobID == jobID {
			out = append(out, f)
		}
	}
	return out, nil
}

func (r *memJobRepo) ListVersionsByJob(_ context.Context, _ uuid.UUID) ([]model.FrameVersion, error) {
	return []model.FrameVersion{}, nil
}

func (r *memJobRepo) ListTasksByJob(_ context.Context, jobID uuid.UUID) ([]model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.FrameTask, 0)
	for _, t := range r.tasks {
		if t.JobID == jobID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *memJobRepo) TransitionTaskStatus(_ context.Context, taskID uuid.UUID, fromStatus, toStatus string, errCode, errDetail *string) (model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return model.FrameTask{}, repository.ErrNotFound
	}
	if t.Status != fromStatus {
		return model.FrameTask{}, repository.ErrInvalidTransition
	}
	t.Status = toStatus
	if toStatus == model.TaskStatusQueued {
		t.ErrorCode = nil
		t.ErrorDetail = nil
	} else {
		if errCode != nil {
			t.ErrorCode = errCode
		}
		if errDetail != nil {
			t.ErrorDetail = errDetail
		}
	}
	r.tasks[taskID] = t
	return t, nil
}

func (r *memJobRepo) IncrementTaskRetry(_ context.Context, taskID uuid.UUID, toStatus string, errCode, errDetail *string) (model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return model.FrameTask{}, repository.ErrNotFound
	}
	t.RetryCount++
	t.Status = toStatus
	t.ErrorCode = errCode
	t.ErrorDetail = errDetail
	r.tasks[taskID] = t
	return t, nil
}

func (r *memJobRepo) GetFrameByID(_ context.Context, frameID uuid.UUID) (model.StoryFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.frames[frameID]
	if !ok {
		return model.StoryFrame{}, repository.ErrNotFound
	}
	return f, nil
}

func (r *memJobRepo) SetFrameActiveVersion(_ context.Context, frameID, versionID uuid.UUID) (model.StoryFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.frames[frameID]
	if !ok {
		return model.StoryFrame{}, repository.ErrNotFound
	}
	f.ActiveVersionID = &versionID
	r.frames[frameID] = f
	return f, nil
}

func (r *memJobRepo) ListFrameVersions(_ context.Context, _ uuid.UUID) ([]model.FrameVersion, error) {
	return []model.FrameVersion{}, nil
}

func (r *memJobRepo) CheckAndFinalizeJobStatus(_ context.Context, jobID uuid.UUID) (model.StoryJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return model.StoryJob{}, false, nil
	}
	return j, false, nil
}

type mockEnqueuer struct {
	mu           sync.Mutex
	storyCalls   []StoryParsePayload
	frameCalls   []FrameRenderPayload
	frameQueues  []string
	frameDelays  []time.Duration
}

func (m *mockEnqueuer) EnqueueStoryParse(_ context.Context, p StoryParsePayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storyCalls = append(m.storyCalls, p)
	return nil
}

func (m *mockEnqueuer) EnqueueFrameRender(_ context.Context, p FrameRenderPayload, queue string, delay time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frameCalls = append(m.frameCalls, p)
	m.frameQueues = append(m.frameQueues, queue)
	m.frameDelays = append(m.frameDelays, delay)
	return nil
}

type stubExecutor struct {
	fn func(ctx context.Context, task model.FrameTask, traceID string) (FrameExecutionResult, error)
}

func (s stubExecutor) ExecuteFrame(ctx context.Context, task model.FrameTask, traceID string) (FrameExecutionResult, error) {
	return s.fn(ctx, task, traceID)
}

func TestRetryDelayExponentialBackoff(t *testing.T) {
	if RetryDelay(1) != 2*time.Second || RetryDelay(2) != 6*time.Second || RetryDelay(3) != 18*time.Second {
		t.Fatalf("unexpected retry delays: %v %v %v", RetryDelay(1), RetryDelay(2), RetryDelay(3))
	}
}

func TestScheduler_SemaphoreConcurrencyLimit(t *testing.T) {
	repo := newMemJobRepo()
	enq := &mockEnqueuer{}
	broker := sse.NewMemoryBroker()

	var current atomic.Int32
	var maxObserved atomic.Int32
	exec := stubExecutor{fn: func(_ context.Context, task model.FrameTask, _ string) (FrameExecutionResult, error) {
		cur := current.Add(1)
		for {
			prev := maxObserved.Load()
			if cur <= prev || maxObserved.CompareAndSwap(prev, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		current.Add(-1)
		return FrameExecutionResult{VersionNo: 1, ImageURL: "https://example.com/1.png", VLMScore: 9.0}, nil
	}}

	sched := New(repo, enq, broker, exec, nil, 2, nil)
	ctx := context.Background()
	jobID := uuid.New()
	frameID := uuid.New()

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		ft, err := repo.CreateFrameTask(ctx, jobID, frameID, model.TaskTypeInitialRender, 2)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sched.EnqueueFrameTask(ctx, ft.ID, "tr_test", QueueDefault); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(task model.FrameTask) {
			defer wg.Done()
			if err := sched.ProcessFrameTask(ctx, FrameRenderPayload{
				TaskID:  task.ID.String(),
				JobID:   jobID.String(),
				FrameID: frameID.String(),
				TraceID: "tr_test",
			}); err != nil {
				t.Errorf("process task: %v", err)
			}
		}(ft)
	}
	wg.Wait()

	if maxObserved.Load() > 2 {
		t.Fatalf("expected semaphore concurrency <= 2, observed %d", maxObserved.Load())
	}
	if maxObserved.Load() == 0 {
		t.Fatalf("expected executor to run")
	}
}

func TestScheduler_OptimisticLockSkipsNonQueued(t *testing.T) {
	repo := newMemJobRepo()
	var calls atomic.Int32
	exec := stubExecutor{fn: func(_ context.Context, _ model.FrameTask, _ string) (FrameExecutionResult, error) {
		calls.Add(1)
		return FrameExecutionResult{}, nil
	}}
	sched := New(repo, &mockEnqueuer{}, sse.NewMemoryBroker(), exec, nil, 3, nil)
	ctx := context.Background()
	ft, _ := repo.CreateFrameTask(ctx, uuid.New(), uuid.New(), model.TaskTypeInitialRender, 2)
	// Leave task in PENDING (not QUEUED) -> ProcessFrameTask must skip cleanly.
	if err := sched.ProcessFrameTask(ctx, FrameRenderPayload{TaskID: ft.ID.String(), JobID: ft.JobID.String()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 executions for non-queued task, got %d", calls.Load())
	}
}

func TestScheduler_SafetyBlockedHaltsZeroRetry(t *testing.T) {
	repo := newMemJobRepo()
	enq := &mockEnqueuer{}
	broker := sse.NewMemoryBroker()
	exec := stubExecutor{fn: func(_ context.Context, _ model.FrameTask, _ string) (FrameExecutionResult, error) {
		return FrameExecutionResult{}, ErrSafetyBlocked
	}}
	sched := New(repo, enq, broker, exec, nil, 3, nil)

	ctx := context.Background()
	jobID := uuid.New()
	ft, _ := repo.CreateFrameTask(ctx, jobID, uuid.New(), model.TaskTypeInitialRender, 2)
	_, _ = sched.EnqueueFrameTask(ctx, ft.ID, "tr_sec", QueueDefault)

	if err := sched.ProcessFrameTask(ctx, FrameRenderPayload{TaskID: ft.ID.String(), JobID: jobID.String(), TraceID: "tr_sec"}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	got, _ := repo.GetFrameTaskByID(ctx, ft.ID)
	if got.Status != model.TaskStatusFailed || got.RetryCount != 0 || got.ErrorCode == nil || *got.ErrorCode != "SAFETY_BLOCKED" {
		t.Fatalf("expected immediate SAFETY_BLOCKED failure with 0 retries, got %+v", got)
	}
	if len(enq.frameCalls) != 1 {
		t.Fatalf("expected only initial enqueue (0 retry enqueues), got %d", len(enq.frameCalls))
	}
	events, _ := broker.Replay(ctx, jobID.String(), "")
	lastEv := events[len(events)-1]
	if lastEv.Event != model.EventFrameFailed {
		t.Fatalf("expected frame_failed SSE event, got %s", lastEv.Event)
	}
}

func TestScheduler_TransientRetriesAndManualRetry(t *testing.T) {
	repo := newMemJobRepo()
	enq := &mockEnqueuer{}
	broker := sse.NewMemoryBroker()
	exec := stubExecutor{fn: func(_ context.Context, _ model.FrameTask, _ string) (FrameExecutionResult, error) {
		return FrameExecutionResult{}, errors.New("upstream 503 timeout")
	}}
	sched := New(repo, enq, broker, exec, nil, 3, nil)

	ctx := context.Background()
	jobID := uuid.New()
	ft, _ := repo.CreateFrameTask(ctx, jobID, uuid.New(), model.TaskTypeInitialRender, 2)
	_, _ = sched.EnqueueFrameTask(ctx, ft.ID, "tr_retry", QueueDefault)
	payload := FrameRenderPayload{TaskID: ft.ID.String(), JobID: jobID.String(), TraceID: "tr_retry"}

	// Attempt 1 -> retry_count=1, delay=2s
	_ = sched.ProcessFrameTask(ctx, payload)
	// Attempt 2 -> retry_count=2, delay=6s
	_ = sched.ProcessFrameTask(ctx, payload)
	// Attempt 3 -> exhausted -> FAILED
	_ = sched.ProcessFrameTask(ctx, payload)

	got, _ := repo.GetFrameTaskByID(ctx, ft.ID)
	if got.Status != model.TaskStatusFailed || got.RetryCount != 2 {
		t.Fatalf("expected FAILED after 2 retries, got %+v", got)
	}
	if len(enq.frameDelays) != 3 || enq.frameDelays[1] != 2*time.Second || enq.frameDelays[2] != 6*time.Second {
		t.Fatalf("unexpected retry backoff delays: %+v", enq.frameDelays)
	}

	// Manual retry from FAILED -> QUEUED on critical queue
	retried, err := sched.RetryFrameTask(ctx, ft.ID, "tr_manual")
	if err != nil {
		t.Fatalf("manual retry failed: %v", err)
	}
	if retried.Status != model.TaskStatusQueued {
		t.Fatalf("expected QUEUED after manual retry, got %s", retried.Status)
	}
	if enq.frameQueues[len(enq.frameQueues)-1] != QueueCritical {
		t.Fatalf("expected manual retry on critical queue")
	}

	// Second manual retry while QUEUED must fail with ErrInvalidTransition
	if _, err := sched.RetryFrameTask(ctx, ft.ID, "tr_manual"); !errors.Is(err, repository.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestRedisIntegration_AsynqWorkerEndToEnd(t *testing.T) {
	strict := os.Getenv("RUN_REDIS_INTEGRATION") == "1"
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		if strict {
			t.Fatalf("RUN_REDIS_INTEGRATION=1 failed to connect redis: %v", err)
		}
		t.Skipf("skipping asynq integration test: %v", err)
	}
	_ = rdb.Close()

	repo := newMemJobRepo()
	broker := sse.NewMemoryBroker()
	doneCh := make(chan string, 1)
	exec := stubExecutor{fn: func(_ context.Context, task model.FrameTask, traceID string) (FrameExecutionResult, error) {
		doneCh <- task.ID.String()
		return FrameExecutionResult{VersionNo: 1, ImageURL: "https://example.com/ok.png", VLMScore: 9.1}, nil
	}}

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: addr})
	defer func() { _ = client.Close() }()

	sched := New(repo, NewAsynqEnqueuer(client), broker, exec, nil, 2, nil)
	srv, mux := NewAsynqServer(addr, sched)
	go func() {
		_ = srv.Run(mux)
	}()
	defer srv.Shutdown()

	ctx := context.Background()
	jobID := uuid.New()
	ft, _ := repo.CreateFrameTask(ctx, jobID, uuid.New(), model.TaskTypeInitialRender, 2)
	if _, err := sched.EnqueueFrameTask(ctx, ft.ID, "tr_asynq", QueueCritical); err != nil {
		t.Fatalf("enqueue frame task: %v", err)
	}

	select {
	case processedID := <-doneCh:
		if processedID != ft.ID.String() {
			t.Fatalf("unexpected task processed: %s", processedID)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for Asynq worker to process frame task")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := repo.GetFrameTaskByID(ctx, ft.ID)
		if got.Status == model.TaskStatusSucceeded {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	got, _ := repo.GetFrameTaskByID(ctx, ft.ID)
	t.Fatalf("expected SUCCEEDED status after worker execution, got %s", got.Status)
}
