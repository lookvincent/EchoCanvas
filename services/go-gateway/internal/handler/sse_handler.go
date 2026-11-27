package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/sse"
)

// SSEHandler streams real-time job execution events over Server-Sent Events.
type SSEHandler struct {
	jobRepo           repository.JobRepository
	broker            sse.Broker
	heartbeatInterval time.Duration
}

// NewSSEHandler constructs an SSEHandler with default 15s heartbeat interval.
func NewSSEHandler(jobRepo repository.JobRepository, broker sse.Broker) *SSEHandler {
	return &SSEHandler{
		jobRepo:           jobRepo,
		broker:            broker,
		heartbeatInterval: 15 * time.Second,
	}
}

// SetHeartbeatInterval overrides the heartbeat period (primarily for deterministic tests).
func (h *SSEHandler) SetHeartbeatInterval(d time.Duration) {
	if d > 0 {
		h.heartbeatInterval = d
	}
}

// StreamJobEvents handles GET /api/v1/jobs/:jobId/events with Last-Event-ID replay.
func (h *SSEHandler) StreamJobEvents(c *gin.Context) {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid job id")
		return
	}

	if _, err := h.jobRepo.GetJobByID(c.Request.Context(), jobID); errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "job not found")
		return
	} else if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to verify job")
		return
	}

	jobKey := jobID.String()
	liveCh, cleanup, err := h.broker.Subscribe(c.Request.Context(), jobKey)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to subscribe event stream")
		return
	}
	defer cleanup()

	lastID := strings.TrimSpace(c.GetHeader("Last-Event-ID"))
	missed, err := h.broker.Replay(c.Request.Context(), jobKey, lastID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to replay missed events")
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	seen := make(map[string]struct{}, len(missed))
	for _, ev := range missed {
		writeSSEEvent(c, ev)
		seen[ev.ID] = struct{}{}
	}
	c.Writer.Flush()

	ticker := time.NewTicker(h.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case ev, ok := <-liveCh:
			if !ok {
				return
			}
			if _, dup := seen[ev.ID]; dup {
				continue
			}
			seen[ev.ID] = struct{}{}
			writeSSEEvent(c, ev)
			c.Writer.Flush()
		case <-ticker.C:
			_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: {\"ts\":\"%s\"}\n\n", model.EventHeartbeat, time.Now().UTC().Format(time.RFC3339))
			c.Writer.Flush()
		}
	}
}

func writeSSEEvent(c *gin.Context, ev model.SSEEvent) {
	if ev.ID != "" {
		_, _ = fmt.Fprintf(c.Writer, "id: %s\n", ev.ID)
	}
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", ev.Event, string(ev.Data))
}
