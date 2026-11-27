package middleware

import (
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
)

const HeaderTraceID = "X-Trace-ID"

// Trace injects or propagates X-Trace-ID and emits structured JSON request logs.
func Trace(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		traceID := strings.TrimSpace(c.GetHeader(HeaderTraceID))
		if traceID == "" {
			traceID = "tr_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		}

		c.Set(response.TraceIDContextKey, traceID)
		c.Writer.Header().Set(HeaderTraceID, traceID)

		c.Next()

		if logger != nil {
			logger.InfoContext(
				c.Request.Context(),
				"http_request",
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"status", c.Writer.Status(),
				"latency_ms", time.Since(start).Milliseconds(),
				"trace_id", traceID,
			)
		}
	}
}
