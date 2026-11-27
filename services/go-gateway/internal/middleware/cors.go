package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	defaultCORSOrigins = "http://localhost:3000,http://127.0.0.1:3000"
	corsAllowMethods   = "GET, POST, PUT, DELETE, OPTIONS"
	corsAllowHeaders   = "Content-Type, Authorization, X-Idempotency-Key, X-Trace-ID, Last-Event-ID"
	corsExposeHeaders  = "X-Trace-ID"
)

// CORS applies origin allowlist headers for the Next.js studio frontend and handles OPTIONS preflight.
func CORS(allowOriginsCSV string) gin.HandlerFunc {
	raw := strings.TrimSpace(allowOriginsCSV)
	if raw == "" {
		raw = defaultCORSOrigins
	}
	allowAll := false
	allowed := make(map[string]bool)
	for _, item := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(item)
		if origin == "" {
			continue
		}
		if origin == "*" {
			allowAll = true
			continue
		}
		allowed[origin] = true
	}

	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		if origin != "" {
			if allowAll {
				c.Header("Access-Control-Allow-Origin", "*")
			} else if allowed[origin] {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			}
			c.Header("Access-Control-Allow-Methods", corsAllowMethods)
			c.Header("Access-Control-Allow-Headers", corsAllowHeaders)
			c.Header("Access-Control-Expose-Headers", corsExposeHeaders)
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
