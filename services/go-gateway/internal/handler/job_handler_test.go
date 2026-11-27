package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/client"
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

func (r *fakeHandlerJobRepo) GetFrameByID(_ context.Context, frameID uuid.UUID) (model.StoryFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.frames[frameID]
	if !ok {
		return model.StoryFrame{}, repository.ErrNotFound
	}
	return f, nil
}

func (r *fakeHandlerJobRepo) SetFrameActiveVersion(_ context.Context, frameID, versionID uuid.UUID) (model.StoryFrame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.frames[frameID]
	if !ok {
		return model.StoryFrame{}, repository.ErrNotFound
	}
	v, ok := r.versions[versionID]
	if !ok || v.FrameID != frameID {
		return model.StoryFrame{}, repository.ErrNotFound
	}
	f.ActiveVersionID = &versionID
	r.frames[frameID] = f
	return f, nil
}

func (r *fakeHandlerJobRepo) ListFrameVersions(_ context.Context, frameID uuid.UUID) ([]model.FrameVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.FrameVersion, 0)
	for _, v := range r.versions {
		if v.FrameID == frameID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VersionNo < out[j].VersionNo })
	return out, nil
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
	sort.Slice(out, func(i, j int) bool { return out[i].FrameIndex < out[j].FrameIndex })
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

func (r *fakeHandlerJobRepo) CheckAndFinalizeJobStatus(_ context.Context, jobID uuid.UUID) (model.StoryJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return model.StoryJob{}, false, repository.ErrNotFound
	}
	return j, false, nil
}

func (r *fakeHandlerJobRepo) ListJobsByProject(_ context.Context, projectID uuid.UUID) ([]model.StoryJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.StoryJob, 0)
	for _, j := range r.jobs {
		if j.ProjectID == projectID {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeHandlerJobRepo) ListEpisodicStatesByJob(_ context.Context, jobID uuid.UUID) ([]model.EpisodicEntityState, error) {
	return []model.EpisodicEntityState{
		{
			ID:         uuid.New(),
			JobID:      jobID,
			FrameIndex: 1,
			EntityKey:  "hero_linshen",
			StateJSON:  json.RawMessage(`{"clothing_state":"dry shoulders","held_items":"open black umbrella"}`),
		},
	}, nil
}

type fakeSidecar struct {
	resumeFn func(ctx context.Context, req client.ResumeWorkflowReq, traceID string) (client.ResumeWorkflowResp, error)
	regenFn  func(ctx context.Context, frameID uuid.UUID, req client.RegenerateFrameReq, traceID string) (client.FrameVersionResp, error)
}

func (f *fakeSidecar) ResumeWorkflow(ctx context.Context, req client.ResumeWorkflowReq, traceID string) (client.ResumeWorkflowResp, error) {
	return f.resumeFn(ctx, req, traceID)
}

func (f *fakeSidecar) RegenerateFrame(ctx context.Context, frameID uuid.UUID, req client.RegenerateFrameReq, traceID string) (client.FrameVersionResp, error) {
	return f.regenFn(ctx, frameID, req, traceID)
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
	return setupJobTestRouterWithSidecar(nil)
}

func setupJobTestRouterWithSidecar(sc SidecarCaller) (*gin.Engine, *fakeHandlerJobRepo, *sse.MemoryBroker, *spyEnqueuer, *SSEHandler) {
	gin.SetMode(gin.TestMode)
	pRepo := newFakeProjectRepo()
	aRepo := newFakeAssetRepo()
	jRepo := newFakeHandlerJobRepo()
	usageRepo := newFakeHandlerUsageRepo()
	broker := sse.NewMemoryBroker()
	enq := &spyEnqueuer{}
	sched := scheduler.New(jRepo, enq, broker, nil, nil, 3, nil)
	sseH := NewSSEHandler(jRepo, broker)
	jh := NewJobHandlerWithSidecar(jRepo, sched, sc, usageRepo, broker)
	jh.SetExportDeps(pRepo, aRepo)
	r := NewRouterWithDeps(RouterDeps{
		PGPinger:         fakePinger{},
		ProjectRepo:      pRepo,
		AssetRepo:        aRepo,
		JobHandler:       jh,
		SSEHandler:       sseH,
		CORSAllowOrigins: "http://localhost:3000",
	})
	return r, jRepo, broker, enq, sseH
}

func TestJobEndpoints_IdempotencyAndRetry(t *testing.T) {
	r, jRepo, _, enq, _ := setupJobTestRouter()
	projID := uuid.New()
	otherProjID := uuid.New()

	wMissing := httptest.NewRecorder()
	r.ServeHTTP(wMissing, httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projID.String()+"/jobs", nil))
	if wMissing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when missing idempotency key, got %d", wMissing.Code)
	}

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

	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+resp1.Data.ID.String(), nil))
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 get job, got %d", wGet.Code)
	}

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

func TestJobEndpoints_ConfirmGatesRegenAndActiveVersion(t *testing.T) {
	ctx := context.Background()
	var sc *fakeSidecar
	var r *gin.Engine
	var jRepo *fakeHandlerJobRepo
	var enq *spyEnqueuer

	sc = &fakeSidecar{
		resumeFn: func(_ context.Context, req client.ResumeWorkflowReq, _ string) (client.ResumeWorkflowResp, error) {
			jid, _ := uuid.Parse(req.JobID)
			if req.GateNo == 1 {
				_, _ = jRepo.CreateFrame(ctx, jid, 1, json.RawMessage(`{"action":"f1"}`))
				_, _ = jRepo.CreateFrame(ctx, jid, 2, json.RawMessage(`{"action":"f2"}`))
				_ = jRepo.UpdateJobStatus(ctx, jid, model.JobStatusWaitingGate, model.JobStageGate2, nil)
				return client.ResumeWorkflowResp{ThreadID: req.ThreadID, Status: "WAITING_GATE", CurrentStage: "GATE_2"}, nil
			}
			_ = jRepo.UpdateJobStatus(ctx, jid, model.JobStatusRunning, model.JobStageRenderFrames, nil)
			return client.ResumeWorkflowResp{ThreadID: req.ThreadID, Status: "RUNNING", CurrentStage: "RENDER_FRAMES"}, nil
		},
		regenFn: func(_ context.Context, frameID uuid.UUID, req client.RegenerateFrameReq, _ string) (client.FrameVersionResp, error) {
			v2ID := uuid.New()
			jRepo.mu.Lock()
			jRepo.versions[v2ID] = model.FrameVersion{
				ID:             v2ID,
				FrameID:        frameID,
				VersionNo:      2,
				PromptSnapshot: "updated prompt",
				ImageURL:       "https://cdn.local/v2.png",
			}
			jRepo.mu.Unlock()
			return client.FrameVersionResp{
				ID:                   v2ID.String(),
				FrameID:              frameID.String(),
				VersionNo:            2,
				PromptSnapshot:       "updated prompt",
				ImageURL:             "https://cdn.local/v2.png",
				CascadedFrameIndices: []int{2},
			}, nil
		},
	}

	r, jRepo, _, enq, _ = setupJobTestRouterWithSidecar(sc)
	projID := uuid.New()
	job, _, _ := jRepo.CreateOrGetByIdempotencyKey(ctx, projID, "gate-k1", "th_gate", "tr_gate", 1.0)
	_ = jRepo.UpdateJobStatus(ctx, job.ID, model.JobStatusWaitingGate, model.JobStageGate1, nil)

	// Confirm Gate 2 while still at GATE_1 -> 409 Conflict
	wBadGate := httptest.NewRecorder()
	r.ServeHTTP(wBadGate, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID.String()+"/gates/2/confirm", nil))
	if wBadGate.Code != http.StatusConflict {
		t.Fatalf("expected 409 when confirming wrong gate, got %d", wBadGate.Code)
	}

	// Confirm Gate 1 -> 200 WAITING_GATE / GATE_2 with 2 frames
	wG1 := httptest.NewRecorder()
	r.ServeHTTP(wG1, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID.String()+"/gates/1/confirm", bytes.NewBufferString(`{}`)))
	if wG1.Code != http.StatusOK {
		t.Fatalf("expected 200 confirming Gate 1, got %d body=%s", wG1.Code, wG1.Body.String())
	}

	// Confirm Gate 2 -> 200 and enqueues Frame 1 on critical, Frame 2 on default
	wG2 := httptest.NewRecorder()
	r.ServeHTTP(wG2, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID.String()+"/gates/2/confirm", bytes.NewBufferString(`{}`)))
	if wG2.Code != http.StatusOK {
		t.Fatalf("expected 200 confirming Gate 2, got %d body=%s", wG2.Code, wG2.Body.String())
	}
	if len(enq.frameQueues) != 2 || enq.frameQueues[0] != scheduler.QueueCritical || enq.frameQueues[1] != scheduler.QueueDefault {
		t.Fatalf("expected [critical default] enqueue priority, got %+v", enq.frameQueues)
	}

	frames, _ := jRepo.ListFramesByJob(ctx, job.ID)
	f1 := frames[0]
	v1ID := uuid.New()
	jRepo.mu.Lock()
	jRepo.versions[v1ID] = model.FrameVersion{ID: v1ID, FrameID: f1.ID, VersionNo: 1, PromptSnapshot: "p1", ImageURL: "https://cdn.local/v1.png"}
	jRepo.mu.Unlock()

	// Gate 3 Regenerate -> 200 with version 2 and cascaded_frame_indices=[2]
	wRegen := httptest.NewRecorder()
	reqRegen := httptest.NewRequest(http.MethodPost, "/api/v1/frames/"+f1.ID.String()+"/regenerate", bytes.NewBufferString(`{"user_edit_note":"warm backlight","cascade_downstream":true}`))
	reqRegen.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wRegen, reqRegen)
	if wRegen.Code != http.StatusOK {
		t.Fatalf("expected 200 regen, got %d body=%s", wRegen.Code, wRegen.Body.String())
	}
	var regenResp struct {
		Data model.RegenerateFrameResponse `json:"data"`
	}
	_ = json.Unmarshal(wRegen.Body.Bytes(), &regenResp)
	if regenResp.Data.Version.VersionNo != 2 || len(regenResp.Data.CascadedFrameIndices) != 1 {
		t.Fatalf("unexpected regen response: %+v", regenResp.Data)
	}

	// Switch active version back to v1 -> 200
	wSwitch := httptest.NewRecorder()
	reqSwitch := httptest.NewRequest(http.MethodPut, "/api/v1/frames/"+f1.ID.String()+"/active-version", bytes.NewBufferString(`{"version_id":"`+v1ID.String()+`"}`))
	reqSwitch.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wSwitch, reqSwitch)
	if wSwitch.Code != http.StatusOK {
		t.Fatalf("expected 200 switching active version, got %d body=%s", wSwitch.Code, wSwitch.Body.String())
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

func TestJobEndpoints_ListStatesExportAndCORS(t *testing.T) {
	ctx := context.Background()
	gin.SetMode(gin.TestMode)
	pRepo := newFakeProjectRepo()
	aRepo := newFakeAssetRepo()
	jRepo := newFakeHandlerJobRepo()
	usageRepo := newFakeHandlerUsageRepo()
	broker := sse.NewMemoryBroker()
	enq := &spyEnqueuer{}
	sched := scheduler.New(jRepo, enq, broker, nil, nil, 3, nil)
	jh := NewJobHandlerWithSidecar(jRepo, sched, nil, usageRepo, broker)
	jh.SetExportDeps(pRepo, aRepo)

	r := NewRouterWithDeps(RouterDeps{
		PGPinger:         fakePinger{},
		ProjectRepo:      pRepo,
		AssetRepo:        aRepo,
		JobHandler:       jh,
		SSEHandler:       NewSSEHandler(jRepo, broker),
		CORSAllowOrigins: "http://localhost:3000",
	})

	// CORS OPTIONS preflight -> 204 with Allow-Origin
	wOpt := httptest.NewRecorder()
	reqOpt := httptest.NewRequest(http.MethodOptions, "/api/v1/projects", nil)
	reqOpt.Header.Set("Origin", "http://localhost:3000")
	r.ServeHTTP(wOpt, reqOpt)
	if wOpt.Code != http.StatusNoContent || wOpt.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("unexpected CORS preflight: code=%d origin=%q", wOpt.Code, wOpt.Header().Get("Access-Control-Allow-Origin"))
	}

	pReq := model.CreateProjectRequest{Title: "Web Export Story", RawStory: "Overpass night scene."}
	_ = pReq.NormalizeAndValidate()
	proj, _ := pRepo.Create(ctx, pReq)
	job, _, _ := jRepo.CreateOrGetByIdempotencyKey(ctx, proj.ID, "web-k1", "th_web", "tr_web", 1.0)
	frame, _ := jRepo.CreateFrame(ctx, job.ID, 1, json.RawMessage(`{"shot_type":"Wide","camera_angle":"Eye","action":"Stand","dialogue":"Hello"}`))
	assetReq := model.CreateAssetRequest{
		AssetType:   "CHARACTER",
		EntityKey:   "hero",
		DisplayName: "Hero",
		ProfileJSON: json.RawMessage(`{"coat":"black"}`),
	}
	_ = assetReq.NormalizeAndValidate()
	_, _ = aRepo.Create(ctx, proj.ID, assetReq)

	// GET /projects/:id/jobs
	wList := httptest.NewRecorder()
	r.ServeHTTP(wList, httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+proj.ID.String()+"/jobs", nil))
	if wList.Code != http.StatusOK || !strings.Contains(wList.Body.String(), job.ID.String()) {
		t.Fatalf("unexpected ListProjectJobs response: code=%d body=%s", wList.Code, wList.Body.String())
	}

	// GET /jobs/:jobId/states
	wStates := httptest.NewRecorder()
	r.ServeHTTP(wStates, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+job.ID.String()+"/states", nil))
	if wStates.Code != http.StatusOK || !strings.Contains(wStates.Body.String(), "hero_linshen") {
		t.Fatalf("unexpected GetJobStates response: code=%d body=%s", wStates.Code, wStates.Body.String())
	}

	// POST /jobs/:jobId/export
	wExp := httptest.NewRecorder()
	reqExp := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID.String()+"/export", bytes.NewBufferString(`{"format":"markdown_json"}`))
	reqExp.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wExp, reqExp)
	if wExp.Code != http.StatusOK {
		t.Fatalf("expected 200 export, got %d body=%s", wExp.Code, wExp.Body.String())
	}
	var expResp struct {
		Data model.ExportJobResponse `json:"data"`
	}
	_ = json.Unmarshal(wExp.Body.Bytes(), &expResp)
	if !strings.Contains(expResp.Data.Markdown, "Web Export Story") || !strings.Contains(expResp.Data.Markdown, "Frame 01") {
		t.Fatalf("unexpected export markdown: %s", expResp.Data.Markdown)
	}
	if expResp.Data.JobID != job.ID || frame.ID == uuid.Nil {
		t.Fatalf("invalid export job id")
	}
}
