package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/gateway"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

// GatewayHandler exposes internal AIGC proxy routes plus user-facing usage and budget endpoints.
type GatewayHandler struct {
	gw        *gateway.AIGCGateway
	usageRepo repository.UsageRepository
	broker    sse.Broker
}

// NewGatewayHandler constructs a GatewayHandler.
func NewGatewayHandler(gw *gateway.AIGCGateway, usageRepo repository.UsageRepository, broker sse.Broker) *GatewayHandler {
	return &GatewayHandler{
		gw:        gw,
		usageRepo: usageRepo,
		broker:    broker,
	}
}

// ProxyChatCompletions handles POST /proxy/v1/chat/completions.
func (h *GatewayHandler) ProxyChatCompletions(c *gin.Context) {
	h.proxyModelRequest(c, "llm", model.CallTypeLLMText)
}

// ProxyImageGenerations handles POST /proxy/v1/images/generations.
func (h *GatewayHandler) ProxyImageGenerations(c *gin.Context) {
	h.proxyModelRequest(c, "vision", model.CallTypeImageGen)
}

func (h *GatewayHandler) proxyModelRequest(c *gin.Context, kind, defaultCallType string) {
	var jobID uuid.UUID
	if rawJobID := strings.TrimSpace(c.GetHeader("X-Job-ID")); rawJobID != "" {
		parsed, err := uuid.Parse(rawJobID)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid X-Job-ID header")
			return
		}
		jobID = parsed
	}

	var frameID *uuid.UUID
	if rawFrameID := strings.TrimSpace(c.GetHeader("X-Frame-ID")); rawFrameID != "" {
		parsed, err := uuid.Parse(rawFrameID)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid X-Frame-ID header")
			return
		}
		frameID = &parsed
	}

	callType := strings.TrimSpace(c.GetHeader("X-Call-Type"))
	if callType == "" {
		callType = defaultCallType
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "failed to read request body")
		return
	}

	resp, err := h.gw.Proxy(c.Request.Context(), kind, gateway.ProxyRequest{
		JobID:       jobID,
		FrameID:     frameID,
		TraceID:     response.GetTraceID(c),
		CallType:    callType,
		Body:        body,
		ContentType: c.GetHeader("Content-Type"),
	})
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		case errors.Is(err, gateway.ErrBudgetExceeded):
			response.Fail(c, http.StatusPaymentRequired, response.CodeBudgetExceeded, "job budget limit exceeded")
		case errors.Is(err, gateway.ErrSafetyBlocked):
			response.Fail(c, http.StatusUnprocessableEntity, response.CodeSafetyBlocked, "SAFETY_BLOCKED")
		case errors.Is(err, gateway.ErrRateLimited):
			response.Fail(c, http.StatusTooManyRequests, response.CodeUpstreamError, "gateway rate limit exceeded")
		case errors.Is(err, gateway.ErrUpstreamUnavailable):
			response.Fail(c, http.StatusBadGateway, response.CodeUpstreamError, "upstream model providers unavailable")
		default:
			response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "gateway proxy failure")
		}
		return
	}

	c.Header("X-Provider-Used", resp.ProviderUsed)
	c.Header("X-Model-Used", resp.ModelUsed)
	c.Header("X-Is-Fallback", strconv.FormatBool(resp.IsFallback))
	c.Data(resp.StatusCode, resp.ContentType, resp.Body)
}

// GetJobUsage handles GET /api/v1/jobs/:jobId/usage.
func (h *GatewayHandler) GetJobUsage(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
		return
	}

	summary, err := h.usageRepo.GetJobUsageSummary(c.Request.Context(), jobID)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to load usage summary")
		return
	}
	response.OK(c, summary)
}

// ApproveBudget handles POST /api/v1/jobs/:jobId/budget/approve.
func (h *GatewayHandler) ApproveBudget(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
		return
	}

	var req model.ApproveBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	if err := req.Validate(); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, err.Error())
		return
	}

	job, err := h.usageRepo.ApproveJobBudget(c.Request.Context(), jobID, req.BudgetLimitUSD)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		return
	}
	if errors.Is(err, repository.ErrBudgetTooLow) {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to approve budget")
		return
	}

	if h.broker != nil {
		_, _ = h.broker.Publish(c.Request.Context(), job.ID.String(), model.EventStageChanged, map[string]any{
			"job_id":           job.ID.String(),
			"trace_id":         response.GetTraceID(c),
			"status":           job.Status,
			"current_stage":    job.CurrentStage,
			"budget_limit_usd": job.BudgetLimitUSD,
			"total_cost_usd":   job.TotalCostUSD,
		})
	}
	response.OK(c, job)
}
