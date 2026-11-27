package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/config"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

type memUsageRepo struct {
	mu      sync.Mutex
	jobs    map[uuid.UUID]model.JobBudgetSnapshot
	entries []model.UsageLedgerEntry
}

func newMemUsageRepo() *memUsageRepo {
	return &memUsageRepo{
		jobs: make(map[uuid.UUID]model.JobBudgetSnapshot),
	}
}

func (m *memUsageRepo) seedJob(jobID uuid.UUID, status string, limitUSD, totalUSD float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[jobID] = model.JobBudgetSnapshot{
		JobID:          jobID,
		Status:         status,
		CurrentStage:   model.JobStageRenderFrames,
		BudgetLimitUSD: limitUSD,
		TotalCostUSD:   totalUSD,
	}
}

func (m *memUsageRepo) RecordUsage(_ context.Context, entry model.UsageLedgerEntry) (model.UsageLedgerEntry, float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.jobs[entry.JobID]
	if !ok {
		return model.UsageLedgerEntry{}, 0, repository.ErrNotFound
	}
	entry.ID = uuid.New()
	entry.CreatedAt = time.Now().UTC()
	m.entries = append(m.entries, entry)
	snap.TotalCostUSD += entry.CostUSD
	m.jobs[entry.JobID] = snap
	return entry, snap.TotalCostUSD, nil
}

func (m *memUsageRepo) GetJobBudgetSnapshot(_ context.Context, jobID uuid.UUID) (model.JobBudgetSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.jobs[jobID]
	if !ok {
		return model.JobBudgetSnapshot{}, repository.ErrNotFound
	}
	return snap, nil
}

func (m *memUsageRepo) MarkJobPausedBudget(_ context.Context, jobID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.jobs[jobID]
	if !ok {
		return repository.ErrNotFound
	}
	snap.Status = model.JobStatusPausedBudget
	m.jobs[jobID] = snap
	return nil
}

func (m *memUsageRepo) ApproveJobBudget(_ context.Context, jobID uuid.UUID, newLimitUSD float64) (model.StoryJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.jobs[jobID]
	if !ok {
		return model.StoryJob{}, repository.ErrNotFound
	}
	if newLimitUSD <= snap.TotalCostUSD {
		return model.StoryJob{}, repository.ErrBudgetTooLow
	}
	snap.BudgetLimitUSD = newLimitUSD
	if snap.Status == model.JobStatusPausedBudget {
		snap.Status = model.JobStatusRunning
	}
	m.jobs[jobID] = snap
	return model.StoryJob{
		ID:             jobID,
		Status:         snap.Status,
		CurrentStage:   snap.CurrentStage,
		BudgetLimitUSD: snap.BudgetLimitUSD,
		TotalCostUSD:   snap.TotalCostUSD,
	}, nil
}

func (m *memUsageRepo) GetJobUsageSummary(_ context.Context, jobID uuid.UUID) (model.JobUsageSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.jobs[jobID]
	if !ok {
		return model.JobUsageSummary{}, repository.ErrNotFound
	}
	var out []model.UsageLedgerEntry
	for _, e := range m.entries {
		if e.JobID == jobID {
			out = append(out, e)
		}
	}
	return model.JobUsageSummary{
		JobID:          jobID,
		Status:         snap.Status,
		BudgetLimitUSD: snap.BudgetLimitUSD,
		TotalCostUSD:   snap.TotalCostUSD,
		Breakdown:      []model.ModelUsageBreakdown{},
		Entries:        out,
	}, nil
}

func TestCircuitBreaker_StateTransitions(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	cb := NewCircuitBreakerWithClock(5, 30*time.Second, func() time.Time { return now })

	if cb.State() != StateClosed || !cb.Allow() {
		t.Fatalf("expected CLOSED initially")
	}

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if cb.State() != StateOpen || cb.Allow() {
		t.Fatalf("expected OPEN after 5 failures and Allow()=false")
	}

	// Advance 31s -> transitions to HALF_OPEN for 1 probe
	now = now.Add(31 * time.Second)
	if cb.State() != StateHalfOpen {
		t.Fatalf("expected HALF_OPEN after cooldown, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatalf("expected first probe Allow()=true")
	}
	if cb.Allow() {
		t.Fatalf("expected concurrent second probe Allow()=false while half-open in flight")
	}

	// Probe fails -> immediately back to OPEN
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatalf("expected OPEN after failed half-open probe, got %s", cb.State())
	}

	// Advance another 31s and probe succeeds -> CLOSED
	now = now.Add(31 * time.Second)
	if !cb.Allow() {
		t.Fatalf("expected probe Allow()=true after second cooldown")
	}
	cb.RecordSuccess()
	if cb.State() != StateClosed {
		t.Fatalf("expected CLOSED after probe success, got %s", cb.State())
	}
}

func TestAIGCGateway_FallbackBreakerAndBilling(t *testing.T) {
	var primaryCalls atomic.Int32
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"primary overloaded"}`))
	}))
	defer primarySrv.Close()

	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"cmpl_fb","usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	}))
	defer fallbackSrv.Close()

	usageRepo := newMemUsageRepo()
	jobID := uuid.New()
	usageRepo.seedJob(jobID, model.JobStatusRunning, 1.00, 0)
	broker := sse.NewMemoryBroker()

	cfg := config.Config{
		LLMPrimary: config.ProviderEndpoint{
			Name:    "primary_p",
			BaseURL: primarySrv.URL,
			Model:   "deepseek-chat",
			APIKey:  "test-primary-key",
		},
		LLMFallback: config.ProviderEndpoint{
			Name:    "fallback_p",
			BaseURL: fallbackSrv.URL,
			Model:   "gpt-4o-mini",
			APIKey:  "test-fallback-key",
		},
	}
	gw := New(usageRepo, broker, cfg, Options{RPM: 600, CBThreshold: 2, CBCooldown: 30 * time.Second})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		resp, err := gw.Proxy(ctx, "llm", ProxyRequest{
			JobID:    jobID,
			TraceID:  "tr_gw_fb",
			CallType: model.CallTypeLLMText,
			Body:     []byte(`{"messages":[]}`),
		})
		if err != nil {
			t.Fatalf("call %d failed unexpectedly: %v", i, err)
		}
		if !resp.IsFallback || resp.ProviderUsed != "fallback_p" || resp.CostUSD <= 0 {
			t.Fatalf("expected fallback billing response, got %+v", resp)
		}
	}

	// Breaker threshold=2: 1st and 2nd calls hit primary, 3rd call skips OPEN breaker
	if primaryCalls.Load() != 2 {
		t.Fatalf("expected primary called 2 times before breaker opened, got %d", primaryCalls.Load())
	}

	summary, _ := usageRepo.GetJobUsageSummary(ctx, jobID)
	if len(summary.Entries) != 3 || summary.TotalCostUSD <= 0 {
		t.Fatalf("expected 3 ledger entries and positive total cost, got %+v", summary)
	}
	events, _ := broker.Replay(ctx, jobID.String(), "")
	if len(events) != 3 || events[0].Event != model.EventCostUpdated {
		t.Fatalf("expected 3 cost_updated SSE events, got %+v", events)
	}
}

func TestAIGCGateway_PreFlightBudgetGuard(t *testing.T) {
	var upstreamHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	usageRepo := newMemUsageRepo()
	jobID := uuid.New()
	usageRepo.seedJob(jobID, model.JobStatusRunning, 0.05, 0.045)
	broker := sse.NewMemoryBroker()

	gw := New(usageRepo, broker, config.Config{
		VisionPrimary: config.ProviderEndpoint{Name: "flux", BaseURL: srv.URL, Model: "flux-dev"},
	}, Options{RPM: 600})

	// IMAGE_GEN pre-flight est=0.04 -> 0.045 + 0.04 > 0.05 -> must block
	_, err := gw.Proxy(context.Background(), "vision", ProxyRequest{
		JobID:    jobID,
		TraceID:  "tr_budget",
		CallType: model.CallTypeImageGen,
		Body:     []byte(`{"prompt":"snow"}`),
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("expected 0 upstream hits when budget exceeded, got %d", upstreamHits.Load())
	}
	snap, _ := usageRepo.GetJobBudgetSnapshot(context.Background(), jobID)
	if snap.Status != model.JobStatusPausedBudget {
		t.Fatalf("expected job status PAUSED_BUDGET, got %s", snap.Status)
	}
}

func TestAIGCGateway_SafetyBlockedDoesNotFallback(t *testing.T) {
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"SAFETY_BLOCKED"}`))
	}))
	defer primarySrv.Close()

	var fallbackHits atomic.Int32
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer fallbackSrv.Close()

	usageRepo := newMemUsageRepo()
	jobID := uuid.New()
	usageRepo.seedJob(jobID, model.JobStatusRunning, 1.0, 0)

	gw := New(usageRepo, sse.NewMemoryBroker(), config.Config{
		LLMPrimary:  config.ProviderEndpoint{Name: "p1", BaseURL: primarySrv.URL, Model: "m1"},
		LLMFallback: config.ProviderEndpoint{Name: "p2", BaseURL: fallbackSrv.URL, Model: "m2"},
	}, Options{RPM: 600})

	_, err := gw.Proxy(context.Background(), "llm", ProxyRequest{
		JobID:    jobID,
		TraceID:  "tr_safe",
		CallType: model.CallTypeLLMText,
		Body:     []byte(`{}`),
	})
	if !errors.Is(err, ErrSafetyBlocked) {
		t.Fatalf("expected ErrSafetyBlocked, got %v", err)
	}
	if fallbackHits.Load() != 0 {
		t.Fatalf("expected 0 fallback attempts on safety block, got %d", fallbackHits.Load())
	}
}

func TestAIGCGateway_RateLimiter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":10}}`))
	}))
	defer srv.Close()

	// RPM=6 -> burst=1, refill=0.1/sec -> immediate 2nd call hits rate limit
	gw := New(nil, nil, config.Config{
		LLMPrimary: config.ProviderEndpoint{Name: "p1", BaseURL: srv.URL, Model: "m1"},
	}, Options{RPM: 6})

	if _, err := gw.Proxy(context.Background(), "llm", ProxyRequest{CallType: model.CallTypeLLMText, Body: []byte(`{}`)}); err != nil {
		t.Fatalf("first call within burst should pass: %v", err)
	}
	_, err := gw.Proxy(context.Background(), "llm", ProxyRequest{CallType: model.CallTypeLLMText, Body: []byte(`{}`)})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited on second immediate call, got %v", err)
	}
}

func TestAIGCGateway_OpenCodeHeaders(t *testing.T) {
	var gotSession, gotUA, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get("x-opencode-session")
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	usageRepo := newMemUsageRepo()
	jobID := uuid.New()
	usageRepo.seedJob(jobID, model.JobStatusRunning, 1.0, 0)

	gw := New(usageRepo, sse.NewMemoryBroker(), config.Config{
		LLMPrimary: config.ProviderEndpoint{
			Name:    "opencodego",
			BaseURL: srv.URL,
			Model:   "deepseek-v4.1-flash",
			APIKey:  "test-key",
		},
	}, Options{RPM: 600})

	resp, err := gw.Proxy(context.Background(), "llm", ProxyRequest{
		JobID:    jobID,
		TraceID:  "tr_opencode",
		CallType: model.CallTypeLLMText,
		Body:     []byte(`{"model":"deepseek-v4.1-flash","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("unexpected error forwarding to opencodego endpoint: %v", err)
	}
	if resp.ProviderUsed != "opencodego" {
		t.Fatalf("expected provider opencodego, got %s", resp.ProviderUsed)
	}
	wantSession := "ses_" + jobID.String()
	if gotSession != wantSession {
		t.Fatalf("expected x-opencode-session %q, got %q", wantSession, gotSession)
	}
	if gotUA != "opencode/1.0" {
		t.Fatalf("expected User-Agent opencode/1.0, got %q", gotUA)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("expected Authorization Bearer test-key, got %q", gotAuth)
	}
}
