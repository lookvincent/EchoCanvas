package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/scheduler"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

type fakeHandlerJobRepo struct {
	mu       sync.Mutex
	jobs     map[uuid.UUID]model.StoryJob
	idemIdx  map[string]uuid.UUID
	frames   map[uuid.UUID]model.StoryFrame
	versions map[uuid.UUID]model.FrameVersion
	tasks    map[uuid.UUID]model.FrameTask
}

func newFakeHandlerJobRepo() *fakeHandlerJobRepo {
	return &fakeHandlerJobRepo{
		jobs:     make(map[uuid.UUID]model.StoryJob),
		idemIdx:  make(map[string]uuid.UUID),
		frames:   make(map[uuid.UUID]model.StoryFrame),
		versions: make(map[uuid.UUID]model.FrameVersion),
		tasks:    make(map[uuid.UUID]model.FrameTask),
	}
}

func (r *fakeHandlerJobRepo) CreateOrGetByIdempotencyKey(_ context.Context, projectID uuid.UUID, key, threadID, traceID string, budget float64) (model.StoryJob, bool, error) {
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

func (r *fakeHandlerJobRepo) GetJobByID(_ context.Context, jobID uuid.UUID) (model.StoryJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return model.StoryJob{}, repository.ErrNotFound
	}
	return j, nil
}

func (r *fakeHandlerJobRepo) UpdateJobStatus(_ context.Context, jobID uuid.UUID, status, stage string, errMsg *string) error {
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

func (r *fakeHandlerJobRepo) CreateFrame(_ context.Context, jobID uuid.UUID, idx int, script json.RawMessage) (model.StoryFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := model.StoryFrame{ID: uuid.New(), JobID: jobID, FrameIndex: idx, ScriptJSON: script}
	r.frames[f.ID] = f
	return f, nil
}

func (r *fakeHandlerJobRepo) CreateFrameTask(_ context.Context, jobID, frameID uuid.UUID, taskType string, maxRetries int) (model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := model.FrameTask{ID: uuid.New(), JobID: jobID, FrameID: frameID, TaskType: taskType, Status: model.TaskStatusPending, MaxRetries: maxRetries}
	r.tasks[t.ID] = t
	return t, nil
}

func (r *fakeHandlerJobRepo) GetFrameTaskByID(_ context.Context, taskID uuid.UUID) (model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return model.FrameTask{}, repository.ErrNotFound
	}
	return t, nil
}

func (r *fakeHandlerJobRepo) ListFramesByJob(_ context.Context, jobID uuid.UUID) ([]model.StoryFrame, error) {
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

func (r *fakeHandlerJobRepo) ListVersionsByJob(_ context.Context, _ uuid.UUID) ([]model.FrameVersion, error) {
	return []model.FrameVersion{}, nil
}

func (r *fakeHandlerJobRepo) ListTasksByJob(_ context.Context, jobID uuid.UUID) ([]model.FrameTask, error) {
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

func (r *fakeHandlerJobRepo) TransitionTaskStatus(_ context.Context, taskID uuid.UUID, fromStatus, toStatus string, errCode, errDetail *string) (model.FrameTask, error) {
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
	r.tasks[taskID] = t
	return t, nil
}

func (r *fakeHandlerJobRepo) IncrementTaskRetry(_ context.Context, taskID uuid.UUID, toStatus string, errCode, errDetail *string) (model.FrameTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tasks[taskID]
	t.RetryCount++
	t.Status = toStatus
	r.tasks[taskID] = t
	return t, nil
}

type spyEnqueuer struct {
	mu          sync.Mutex
	storyCount  int
	frameQueues []string
}

func (s *spyEnqueuer) EnqueueStoryParse(_ context.Context, _ scheduler.StoryParsePayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storyCount++
	return nil
}

func (s *spyEnqueuer) EnqueueFrameRender(_ context.Context, _ scheduler.FrameRenderPayload, queue string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frameQueues = append(s.frameQueues, queue)
	return nil
}

func setupJobTestRouter() (*gin.Engine, *fakeHandlerJobRepo, *sse.MemoryBroker, *spyEnqueuer, *SSEHandler) {
	gin.SetMode(gin.TestMode)
	pRepo := newFakeProjectRepo()
	aRepo := newFakeAssetRepo()
	jRepo := newFakeHandlerJobRepo()
	broker := sse.NewMemoryBroker()
	enq := &spyEnqueuer{}
	sched := scheduler.New(jRepo, enq, broker, nil, nil, 3, nil)
	sseH := NewSSEHandler(jRepo, broker)
	r := NewRouterWithDeps(RouterDeps{
		PGPinger:    fakePinger{},
		ProjectRepo: pRepo,
		AssetRepo:   aRepo,
		JobHandler:  NewJobHandler(jRepo, sched),
		SSEHandler:  sseH,
	})
	return r, jRepo, broker, enq, sseH
}

func TestJobEndpoints_IdempotencyAndRetry(t *testing.T) {
	r, jRepo, _, enq, _ := setupJobTestRouter()
	projID := uuid.New()
	otherProjID := uuid.New()

	// Missing X-Idempotency-Key -> 40001
	wMissing := httptest.NewRecorder()
	r.ServeHTTP(wMissing, httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projID.String()+"/jobs", nil))
	if wMissing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when missing idempotency key, got %d", wMissing.Code)
	}

	// First create -> 201 and 1 enqueue
	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projID.String()+"/jobs", bytes.NewBufferString(`{"budget_limit_usd":1.25}`))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Idempotency-Key", "idem-001")
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w1.Code, w1.Body.String())
	}
	var resp1 struct {
		Data model.StoryJob `json:"data"`
	}
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)

	// Duplicate submit with same key -> 200, same job, still 1 enqueue total
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projID.String()+"/jobs", bytes.NewBufferString(`{"budget_limit_usd":1.25}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Idempotency-Key", "idem-001")
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 on duplicate key, got %d", w2.Code)
	}
	var resp2 struct {
		Data model.StoryJob `json:"data"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.Data.ID != resp1.Data.ID || enq.storyCount != 1 {
		t.Fatalf("idempotency failed: job1=%s job2=%s enqueues=%d", resp1.Data.ID, resp2.Data.ID, enq.storyCount)
	}

	// Same key on different project -> 40901
	wConflict := httptest.NewRecorder()
	reqConflict := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+otherProjID.String()+"/jobs", nil)
	reqConflict.Header.Set("X-Idempotency-Key", "idem-001")
	r.ServeHTTP(wConflict, reqConflict)
	if wConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d", wConflict.Code)
	}
	var envErr response.Envelope
	_ = json.Unmarshal(wConflict.Body.Bytes(), &envErr)
	if envErr.Code != response.CodeConflict {
		t.Fatalf("expected error code %d, got %d", response.CodeConflict, envErr.Code)
	}

	// Get job detail -> 200
	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+resp1.Data.ID.String(), nil))
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 get job, got %d", wGet.Code)
	}

	// Retry task: non-FAILED returns 409, FAILED returns 200 and enqueues critical
	ctx := context.Background()
	ft, _ := jRepo.CreateFrameTask(ctx, resp1.Data.ID, uuid.New(), model.TaskTypeInitialRender, 2)
	wRetryBad := httptest.NewRecorder()
	r.ServeHTTP(wRetryBad, httptest.NewRequest(http.MethodPost, "/api/v1/frame-tasks/"+ft.ID.String()+"/retry", nil))
	if wRetryBad.Code != http.StatusConflict {
		t.Fatalf("expected 409 retrying PENDING task, got %d", wRetryBad.Code)
	}

	_, _ = jRepo.TransitionTaskStatus(ctx, ft.ID, model.TaskStatusPending, model.TaskStatusFailed, nil, nil)
	wRetryOK := httptest.NewRecorder()
	r.ServeHTTP(wRetryOK, httptest.NewRequest(http.MethodPost, "/api/v1/frame-tasks/"+ft.ID.String()+"/retry", nil))
	if wRetryOK.Code != http.StatusOK {
		t.Fatalf("expected 200 retrying FAILED task, got %d", wRetryOK.Code)
	}
	if len(enq.frameQueues) != 1 || enq.frameQueues[0] != scheduler.QueueCritical {
		t.Fatalf("expected manual retry enqueued on critical queue, got %+v", enq.frameQueues)
	}
}

func TestSSEStream_ReplayLiveAndHeartbeat(t *testing.T) {
	r, jRepo, broker, _, sseH := setupJobTestRouter()
	sseH.SetHeartbeatInterval(40 * time.Millisecond)

	job, _, _ := jRepo.CreateOrGetByIdempotencyKey(context.Background(), uuid.New(), "sse-k1", "th_1", "tr_1", 0.5)
	ev1, _ := broker.Publish(context.Background(), job.ID.String(), model.EventStageChanged, map[string]any{"s": 1})
	ev2, _ := broker.Publish(context.Background(), job.ID.String(), model.EventFrameProgress, map[string]any{"s": 2})

	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/jobs/"+job.ID.String()+"/events", nil)
	req.Header.Set("Last-Event-ID", ev1.ID)

	go func() {
		time.Sleep(60 * time.Millisecond)
		_, _ = broker.Publish(context.Background(), job.ID.String(), model.EventFrameCompleted, map[string]any{"s": 3})
	}()

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("sse request failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	body := buf.String()

	if strings.Contains(body, ev1.ID) {
		t.Fatalf("did not expect ev1 %s when Last-Event-ID=%s; body=%s", ev1.ID, ev1.ID, body)
	}
	if !strings.Contains(body, ev2.ID) || !strings.Contains(body, model.EventFrameCompleted) || !strings.Contains(body, model.EventHeartbeat) {
		t.Fatalf("expected replayed ev2, live frame_completed, and heartbeat; got body=%s", body)
	}
}
