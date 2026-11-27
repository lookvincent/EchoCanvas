package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/config"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
	"golang.org/x/time/rate"
)

var (
	ErrBudgetExceeded      = errors.New("job budget exceeded")
	ErrSafetyBlocked       = errors.New("upstream content safety policy blocked")
	ErrUpstreamUnavailable = errors.New("all upstream model providers unavailable")
	ErrRateLimited         = errors.New("gateway provider rate limit exceeded")
)

// RouteConfig binds primary and fallback endpoints for a model route kind.
type RouteConfig struct {
	Primary  config.ProviderEndpoint
	Fallback config.ProviderEndpoint
	Path     string
}

// ProxyRequest encapsulates an internal model call forwarded through AIGCGateway.
type ProxyRequest struct {
	JobID       uuid.UUID
	FrameID     *uuid.UUID
	TraceID     string
	CallType    string
	Body        []byte
	ContentType string
}

// ProxyResponse carries upstream response details plus billing metadata.
type ProxyResponse struct {
	StatusCode   int
	Body         []byte
	ContentType  string
	ProviderUsed string
	ModelUsed    string
	IsFallback   bool
	CostUSD      float64
	TotalCostUSD float64
}

// AIGCGateway enforces budget, rate limiting, circuit breaking, fallback routing, and usage accounting.
type AIGCGateway struct {
	usageRepo   repository.UsageRepository
	broker      sse.Broker
	httpClient  *http.Client
	routes      map[string]RouteConfig
	mu          sync.Mutex
	breakers    map[string]*CircuitBreaker
	limiters    map[string]*rate.Limiter
	cbThreshold int
	cbCooldown  time.Duration
	rpm         int
	now         func() time.Time
	logger      *slog.Logger
}

// Options configures optional gateway dependencies and test hooks.
type Options struct {
	HTTPClient  *http.Client
	RPM         int
	CBThreshold int
	CBCooldown  time.Duration
	Clock       func() time.Time
	Logger      *slog.Logger
}

// New constructs an AIGCGateway with primary/fallback routes from Config.
func New(usageRepo repository.UsageRepository, broker sse.Broker, cfg config.Config, opts Options) *AIGCGateway {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 90 * time.Second}
	}
	rpm := opts.RPM
	if rpm <= 0 {
		rpm = cfg.GatewayRPM
	}
	if rpm <= 0 {
		rpm = 60
	}
	cbThreshold := opts.CBThreshold
	if cbThreshold <= 0 {
		cbThreshold = cfg.CircuitBreakerThreshold
	}
	if cbThreshold <= 0 {
		cbThreshold = 5
	}
	cbCooldown := opts.CBCooldown
	if cbCooldown <= 0 {
		cbCooldown = time.Duration(cfg.CircuitBreakerCooldownSec) * time.Second
	}
	if cbCooldown <= 0 {
		cbCooldown = 30 * time.Second
	}
	nowFn := opts.Clock
	if nowFn == nil {
		nowFn = time.Now
	}

	return &AIGCGateway{
		usageRepo:  usageRepo,
		broker:     broker,
		httpClient: httpClient,
		routes: map[string]RouteConfig{
			"llm": {
				Primary:  cfg.LLMPrimary,
				Fallback: cfg.LLMFallback,
				Path:     "/chat/completions",
			},
			"vision": {
				Primary:  cfg.VisionPrimary,
				Fallback: cfg.VisionFallback,
				Path:     "/images/generations",
			},
		},
		breakers:    make(map[string]*CircuitBreaker),
		limiters:    make(map[string]*rate.Limiter),
		cbThreshold: cbThreshold,
		cbCooldown:  cbCooldown,
		rpm:         rpm,
		now:         nowFn,
		logger:      opts.Logger,
	}
}

func (g *AIGCGateway) breakerFor(providerKey string) *CircuitBreaker {
	g.mu.Lock()
	defer g.mu.Unlock()
	cb, ok := g.breakers[providerKey]
	if !ok {
		cb = NewCircuitBreakerWithClock(g.cbThreshold, g.cbCooldown, g.now)
		g.breakers[providerKey] = cb
	}
	return cb
}

func (g *AIGCGateway) limiterFor(providerKey string) *rate.Limiter {
	g.mu.Lock()
	defer g.mu.Unlock()
	lim, ok := g.limiters[providerKey]
	if !ok {
		perSec := rate.Limit(float64(g.rpm) / 60.0)
		burst := g.rpm / 6
		if burst < 1 {
			burst = 1
		}
		lim = rate.NewLimiter(perSec, burst)
		g.limiters[providerKey] = lim
	}
	return lim
}

// Proxy executes pre-flight budget checks, resilient primary/fallback forwarding, and ledger accounting.
func (g *AIGCGateway) Proxy(ctx context.Context, kind string, req ProxyRequest) (ProxyResponse, error) {
	route, ok := g.routes[kind]
	if !ok {
		return ProxyResponse{}, fmt.Errorf("unknown proxy route kind: %s", kind)
	}

	if req.JobID != uuid.Nil && g.usageRepo != nil {
		snap, err := g.usageRepo.GetJobBudgetSnapshot(ctx, req.JobID)
		if err != nil {
			return ProxyResponse{}, err
		}
		est := EstimatePreFlightMaxCost(req.CallType, route.Primary.Model)
		if snap.Status == model.JobStatusPausedBudget || snap.TotalCostUSD+est > snap.BudgetLimitUSD {
			_ = g.usageRepo.MarkJobPausedBudget(ctx, req.JobID)
			if g.broker != nil {
				_, _ = g.broker.Publish(ctx, req.JobID.String(), model.EventStageChanged, map[string]any{
					"job_id":           req.JobID.String(),
					"trace_id":         req.TraceID,
					"status":           model.JobStatusPausedBudget,
					"current_stage":    snap.CurrentStage,
					"budget_limit_usd": snap.BudgetLimitUSD,
					"total_cost_usd":   snap.TotalCostUSD,
				})
			}
			return ProxyResponse{}, ErrBudgetExceeded
		}
	}

	// Attempt primary provider
	primaryAttempted := false
	primaryRateLimited := false
	if route.Primary.BaseURL != "" {
		cb := g.breakerFor(kind + ":primary:" + route.Primary.Name)
		lim := g.limiterFor(kind + ":primary:" + route.Primary.Name)
		if cb.Allow() {
			if lim.Allow() {
				primaryAttempted = true
				resp, latencyMS, err := g.doForward(ctx, route.Primary, route.Path, req)
				if err == nil {
					cb.RecordSuccess()
					return g.finalizeSuccess(ctx, route.Primary, req, resp, latencyMS, false)
				}
				if errors.Is(err, ErrSafetyBlocked) {
					return ProxyResponse{}, ErrSafetyBlocked
				}
				cb.RecordFailure()
			} else {
				primaryRateLimited = true
			}
		}
	}

	// Fallback path
	if route.Fallback.BaseURL != "" {
		fbCB := g.breakerFor(kind + ":fallback:" + route.Fallback.Name)
		fbLim := g.limiterFor(kind + ":fallback:" + route.Fallback.Name)
		if fbCB.Allow() && fbLim.Allow() {
			resp, latencyMS, err := g.doForward(ctx, route.Fallback, route.Path, req)
			if err == nil {
				fbCB.RecordSuccess()
				return g.finalizeSuccess(ctx, route.Fallback, req, resp, latencyMS, true)
			}
			if errors.Is(err, ErrSafetyBlocked) {
				return ProxyResponse{}, ErrSafetyBlocked
			}
			fbCB.RecordFailure()
		}
	}

	if primaryRateLimited && !primaryAttempted && route.Fallback.BaseURL == "" {
		return ProxyResponse{}, ErrRateLimited
	}
	return ProxyResponse{}, ErrUpstreamUnavailable
}

type rawUpstreamResp struct {
	statusCode  int
	body        []byte
	contentType string
}

func (g *AIGCGateway) doForward(ctx context.Context, ep config.ProviderEndpoint, path string, req ProxyRequest) (rawUpstreamResp, int, error) {
	targetURL := strings.TrimRight(ep.BaseURL, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(req.Body))
	if err != nil {
		return rawUpstreamResp{}, 0, err
	}
	ct := req.ContentType
	if ct == "" {
		ct = "application/json"
	}
	httpReq.Header.Set("Content-Type", ct)
	if ep.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	if req.TraceID != "" {
		httpReq.Header.Set("X-Trace-ID", req.TraceID)
	}

	start := time.Now()
	httpResp, err := g.httpClient.Do(httpReq)
	latencyMS := int(time.Since(start).Milliseconds())
	if err != nil {
		return rawUpstreamResp{}, latencyMS, err
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 10<<20))
	if err != nil {
		return rawUpstreamResp{}, latencyMS, err
	}

	if isSafetyRejected(httpResp.StatusCode, body) {
		return rawUpstreamResp{}, latencyMS, ErrSafetyBlocked
	}
	if httpResp.StatusCode == http.StatusTooManyRequests || httpResp.StatusCode >= 500 {
		return rawUpstreamResp{}, latencyMS, fmt.Errorf("upstream status %d", httpResp.StatusCode)
	}
	if httpResp.StatusCode >= 400 {
		return rawUpstreamResp{}, latencyMS, fmt.Errorf("upstream client error status %d", httpResp.StatusCode)
	}

	respCT := httpResp.Header.Get("Content-Type")
	if respCT == "" {
		respCT = "application/json"
	}
	return rawUpstreamResp{
		statusCode:  httpResp.StatusCode,
		body:        body,
		contentType: respCT,
	}, latencyMS, nil
}

func isSafetyRejected(statusCode int, body []byte) bool {
	if statusCode == http.StatusUnprocessableEntity {
		return true
	}
	low := strings.ToLower(string(body))
	return strings.Contains(low, "safety_blocked") ||
		strings.Contains(low, "content_policy_violation") ||
		strings.Contains(low, "safety_filter")
}

func (g *AIGCGateway) finalizeSuccess(
	ctx context.Context,
	ep config.ProviderEndpoint,
	req ProxyRequest,
	raw rawUpstreamResp,
	latencyMS int,
	isFallback bool,
) (ProxyResponse, error) {
	inTok, outTok, imgCount := extractUsageMetrics(req.CallType, raw.body)
	costUSD := CalculateCost(ep.Name, ep.Model, req.CallType, inTok, outTok, imgCount)

	var totalCost float64
	if req.JobID != uuid.Nil && g.usageRepo != nil {
		_, total, err := g.usageRepo.RecordUsage(ctx, model.UsageLedgerEntry{
			JobID:        req.JobID,
			FrameID:      req.FrameID,
			TraceID:      req.TraceID,
			Provider:     ep.Name,
			Model:        ep.Model,
			CallType:     req.CallType,
			InputTokens:  inTok,
			OutputTokens: outTok,
			ImageCount:   imgCount,
			LatencyMS:    latencyMS,
			CostUSD:      costUSD,
			IsFallback:   isFallback,
		})
		if err != nil {
			return ProxyResponse{}, err
		}
		totalCost = total
		if g.broker != nil {
			_, _ = g.broker.Publish(ctx, req.JobID.String(), model.EventCostUpdated, map[string]any{
				"job_id":             req.JobID.String(),
				"trace_id":           req.TraceID,
				"provider":           ep.Name,
				"model":              ep.Model,
				"call_type":          req.CallType,
				"step_cost_usd":      costUSD,
				"job_total_cost_usd": totalCost,
				"is_fallback":        isFallback,
			})
		}
	}

	return ProxyResponse{
		StatusCode:   raw.statusCode,
		Body:         raw.body,
		ContentType:  raw.contentType,
		ProviderUsed: ep.Name,
		ModelUsed:    ep.Model,
		IsFallback:   isFallback,
		CostUSD:      costUSD,
		TotalCostUSD: totalCost,
	}, nil
}

func extractUsageMetrics(callType string, body []byte) (int, int, int) {
	var parsed struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
		Data   []json.RawMessage `json:"data"`
		Images []json.RawMessage `json:"images"`
	}
	_ = json.Unmarshal(body, &parsed)

	inTok := parsed.Usage.PromptTokens
	if inTok == 0 {
		inTok = parsed.Usage.InputTokens
	}
	outTok := parsed.Usage.CompletionTokens
	if outTok == 0 {
		outTok = parsed.Usage.OutputTokens
	}

	imgCount := 0
	if callType == model.CallTypeImageGen {
		imgCount = len(parsed.Data)
		if imgCount == 0 {
			imgCount = len(parsed.Images)
		}
		if imgCount == 0 {
			imgCount = 1
		}
	}
	return inTok, outTok, imgCount
}
