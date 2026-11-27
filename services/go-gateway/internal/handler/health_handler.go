package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
)

// Pinger checks downstream storage readiness.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler serves liveness and readiness checks.
type HealthHandler struct {
	pgPinger Pinger
}

// NewHealthHandler constructs a HealthHandler.
func NewHealthHandler(pgPinger Pinger) *HealthHandler {
	return &HealthHandler{pgPinger: pgPinger}
}

// Check handles GET /healthz.
func (h *HealthHandler) Check(c *gin.Context) {
	pgState := "ok"
	if h.pgPinger != nil {
		if err := h.pgPinger.Ping(c.Request.Context()); err != nil {
			response.Fail(c, http.StatusServiceUnavailable, response.CodeInternal, "postgres unavailable")
			return
		}
	} else {
		pgState = "unconfigured"
	}

	response.OK(c, gin.H{
		"status":   "ok",
		"postgres": pgState,
	})
}
