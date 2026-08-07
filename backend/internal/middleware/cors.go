package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS returns middleware that allows cross-origin requests from the given
// origins. The frontend SPA is served from a different origin (e.g.
// http://localhost:5173) than the API, so CORS headers are required.
// When allowedOrigins is empty or contains "*", all origins are allowed
// (dev convenience); production should enumerate explicit origins.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := len(allowedOrigins) == 0
	originSet := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o == "*" {
			allowAll = true
		}
		originSet[o] = true
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowed := allowAll || originSet[origin]

		if allowed && origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
			c.Header("Access-Control-Max-Age", "3600")
			// Bearer-token auth: credentials (cookies) not needed, so keep false.
			c.Header("Access-Control-Allow-Credentials", "false")
		}

		// Short-circuit preflight.
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// parseOrigins splits a comma-separated origin list and trims whitespace.
func parseOrigins(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
