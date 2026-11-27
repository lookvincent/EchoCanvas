package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
)

const HeaderInternalToken = "X-Internal-Token"

// InternalAuth validates shared token authentication for /proxy/v1/* endpoints.
func InternalAuth(expectedToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := strings.TrimSpace(expectedToken)
		provided := strings.TrimSpace(c.GetHeader(HeaderInternalToken))
		if expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			response.Fail(c, http.StatusUnauthorized, 40101, "unauthorized internal token")
			c.Abort()
			return
		}
		c.Next()
	}
}
