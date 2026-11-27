package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	CodeOK             = 0
	CodeInvalidParam   = 40001
	CodeBudgetExceeded = 40201
	CodeNotFound       = 40401
	CodeConflict       = 40901
	CodeSafetyBlocked  = 42201
	CodeInternal       = 50001
	CodeUpstreamError  = 50201
)

// TraceIDContextKey stores the unified request trace ID inside gin.Context.
const TraceIDContextKey = "trace_id"

// Envelope defines the standardized API response format across EchoCanvas.
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
	Data    any    `json:"data,omitempty"`
}

// GetTraceID retrieves the trace ID from the Gin context.
func GetTraceID(c *gin.Context) string {
	if v, ok := c.Get(TraceIDContextKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// OK writes a 200 JSON response wrapped in the standard envelope.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{
		Code:    CodeOK,
		Message: "ok",
		TraceID: GetTraceID(c),
		Data:    data,
	})
}

// Created writes a 201 JSON response wrapped in the standard envelope.
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Envelope{
		Code:    CodeOK,
		Message: "ok",
		TraceID: GetTraceID(c),
		Data:    data,
	})
}

// Fail writes an error JSON response wrapped in the standard envelope.
func Fail(c *gin.Context, httpStatus int, code int, msg string) {
	c.JSON(httpStatus, Envelope{
		Code:    code,
		Message: msg,
		TraceID: GetTraceID(c),
	})
}
