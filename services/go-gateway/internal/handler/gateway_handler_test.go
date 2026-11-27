package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/config"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/gateway"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

type fakeHandlerUsageRepo struct {
	mu      sync.Mutex
	budgets map[uuid.UUID]model.JobBudgetSnapshot
	entries []model.UsageLedgerEntry
}

func newFakeHandlerUsageRepo() *fakeHandlerUsageRepo {
	return &fakeHandlerUsageRepo{budgets: make(map[uuid.UUID]model.JobBudgetSnapshot)}
}

func (f *fakeHandlerUsageRepo) RecordUsage(_ context.Context, entry model.UsageLedgerEntry) (model.UsageLedgerEntry, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.budgets[entry.JobID]
	if !ok {
		return model.UsageLedgerEntry{}, 0, repository.ErrNotFound
	}
	entry.ID = uuid.New()
	f.entries = append(f.entries, entry)
	snap.TotalCostUSD += entry.CostUSD
	f.budgets[entry.JobID] = snap
	return entry, snap.TotalCostUSD, nil
}

func (f *fakeHandlerUsageRepo) GetJobBudgetSnapshot(_ context.Context, jobID uuid.UUID) (model.JobBudgetSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.budgets[jobID]
	if !ok {
		return model.JobBudgetSnapshot{}, repository.ErrNotFound
	}
	return snap, nil
}

func (f *fakeHandlerUsageRepo) MarkJobPausedBudget(_ context.Context, jobID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.budgets[jobID]
	if !ok {
		return repository.ErrNotFound
	}
	snap.Status = model.JobStatusPausedBudget
	f.budgets[jobID] = snap
	return nil
}

func (f *fakeHandlerUsageRepo) ApproveJobBudget(_ context.Context, jobID uuid.UUID, newLimitUSD float64) (model.StoryJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.budgets[jobID]
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
	f.budgets[jobID] = snap
	return model.StoryJob{
		ID:             jobID,
		Status:         snap.Status,
		CurrentStage:   snap.CurrentStage,
		BudgetLimitUSD: snap.BudgetLimitUSD,
		TotalCostUSD:   snap.TotalCostUSD,
	}, nil
}

func (f *fakeHandlerUsageRepo) GetJobUsageSummary(_ context.Context, jobID uuid.UUID) (model.JobUsageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.budgets[jobID]
	if !ok {
		return model.JobUsageSummary{}, repository.ErrNotFound
	}
	return model.JobUsageSummary{
		JobID:          jobID,
		Status:         snap.Status,
		BudgetLimitUSD: snap.BudgetLimitUSD,
		TotalCostUSD:   snap.TotalCostUSD,
		Breakdown:      []model.ModelUsageBreakdown{},
		Entries:        f.entries,
	}, nil
}

func TestGatewayProxyEndpoints_AuthAndStatusCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":200}}`))
	}))
	defer llmSrv.Close()

	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"url":"https://img.example.com/1.png"}]}`))
	}))
	defer visionSrv.Close()

	usageRepo := newFakeHandlerUsageRepo()
	jobID := uuid.New()
	usageRepo.budgets[jobID] = model.JobBudgetSnapshot{
		JobID:          jobID,
		Status:         model.JobStatusRunning,
		CurrentStage:   model.JobStageRenderFrames,
		BudgetLimitUSD: 1.0,
		TotalCostUSD:   0,
	}

	broker := sse.NewMemoryBroker()
	gw := gateway.New(usageRepo, broker, config.Config{
		LLMPrimary:    config.ProviderEndpoint{Name: "deepseek", BaseURL: llmSrv.URL, Model: "deepseek-chat"},
		VisionPrimary: config.ProviderEndpoint{Name: "siliconflow", BaseURL: visionSrv.URL, Model: "flux-dev"},
	}, gateway.Options{RPM: 600})

	const internalSecret = "test-internal-secret"
	r := NewRouterWithDeps(RouterDeps{
		PGPinger:          fakePinger{},
		ProjectRepo:       newFakeProjectRepo(),
		AssetRepo:         newFakeAssetRepo(),
		GatewayHandler:    NewGatewayHandler(gw, usageRepo, broker),
		InternalAuthToken: internalSecret,
	})

	// Unauthorized without X-Internal-Token -> 401
	wUnauth := httptest.NewRecorder()
	r.ServeHTTP(wUnauth, httptest.NewRequest(http.MethodPost, "/proxy/v1/chat/completions", bytes.NewBufferString(`{}`)))
	if wUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without internal token, got %d", wUnauth.Code)
	}

	// Authorized chat completion -> 200
	wLLM := httptest.NewRecorder()
	reqLLM := httptest.NewRequest(http.MethodPost, "/proxy/v1/chat/completions", bytes.NewBufferString(`{"messages":[]}`))
	reqLLM.Header.Set("X-Internal-Token", internalSecret)
	reqLLM.Header.Set("X-Job-ID", jobID.String())
	reqLLM.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wLLM, reqLLM)
	if wLLM.Code != http.StatusOK || wLLM.Header().Get("X-Provider-Used") != "deepseek" {
		t.Fatalf("expected 200 from deepseek, got code=%d provider=%s body=%s", wLLM.Code, wLLM.Header().Get("X-Provider-Used"), wLLM.Body.String())
	}

	// Authorized image generation -> 200
	wImg := httptest.NewRecorder()
	reqImg := httptest.NewRequest(http.MethodPost, "/proxy/v1/images/generations", bytes.NewBufferString(`{"prompt":"snow"}`))
	reqImg.Header.Set("X-Internal-Token", internalSecret)
	reqImg.Header.Set("X-Job-ID", jobID.String())
	r.ServeHTTP(wImg, reqImg)
	if wImg.Code != http.StatusOK || wImg.Header().Get("X-Provider-Used") != "siliconflow" {
		t.Fatalf("expected 200 from siliconflow, got code=%d", wImg.Code)
	}

	// GET /usage -> 200 with 2 entries
	wUsage := httptest.NewRecorder()
	r.ServeHTTP(wUsage, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID.String()+"/usage", nil))
	if wUsage.Code != http.StatusOK {
		t.Fatalf("expected 200 on usage, got %d", wUsage.Code)
	}
	var usageResp struct {
		Code int                   `json:"code"`
		Data model.JobUsageSummary `json:"data"`
	}
	_ = json.Unmarshal(wUsage.Body.Bytes(), &usageResp)
	if len(usageResp.Data.Entries) != 2 || usageResp.Data.TotalCostUSD <= 0 {
		t.Fatalf("expected 2 ledger entries and positive total cost, got %+v", usageResp.Data)
	}

	// Force PAUSED_BUDGET -> next proxy returns 402 Payment Required
	_ = usageRepo.MarkJobPausedBudget(context.Background(), jobID)
	wPaused := httptest.NewRecorder()
	reqPaused := httptest.NewRequest(http.MethodPost, "/proxy/v1/images/generations", bytes.NewBufferString(`{}`))
	reqPaused.Header.Set("X-Internal-Token", internalSecret)
	reqPaused.Header.Set("X-Job-ID", jobID.String())
	r.ServeHTTP(wPaused, reqPaused)
	if wPaused.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 when budget paused, got %d", wPaused.Code)
	}
	var envErr response.Envelope
	_ = json.Unmarshal(wPaused.Body.Bytes(), &envErr)
	if envErr.Code != response.CodeBudgetExceeded {
		t.Fatalf("expected CodeBudgetExceeded %d, got %d", response.CodeBudgetExceeded, envErr.Code)
	}

	// Approve higher budget -> 200 and resumes RUNNING
	wApprove := httptest.NewRecorder()
	reqApprove := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/budget/approve", bytes.NewBufferString(`{"budget_limit_usd":2.5}`))
	reqApprove.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wApprove, reqApprove)
	if wApprove.Code != http.StatusOK {
		t.Fatalf("expected 200 on budget approve, got %d body=%s", wApprove.Code, wApprove.Body.String())
	}
	var approvedResp struct {
		Data model.StoryJob `json:"data"`
	}
	_ = json.Unmarshal(wApprove.Body.Bytes(), &approvedResp)
	if approvedResp.Data.Status != model.JobStatusRunning || approvedResp.Data.BudgetLimitUSD != 2.5 {
		t.Fatalf("expected RUNNING and limit 2.5, got %+v", approvedResp.Data)
	}
}
