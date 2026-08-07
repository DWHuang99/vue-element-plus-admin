package ratelimit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// rateLimitResponse is the uniform 429 response body.
func rateLimitResponse() gin.H {
	return gin.H{"error": gin.H{
		"code":    "RATE_LIMITED",
		"message": "请求过于频繁，请稍后重试",
	}}
}

// GinIPRateLimit returns middleware that limits requests by client IP.
// IP is derived from c.ClientIP() (single-instance direct deployment;
// see tasks T019/T027 for the trusted-proxy note).
func GinIPRateLimit(l *Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := "ip:" + c.ClientIP()
		if !l.Allow(key) {
			// Audit event (FR-014): rate-limit triggers are security events.
			slog.Warn("rate limit triggered", "dimension", "ip", "key", c.ClientIP(),
				"request_id", c.GetString("X-Request-Id"))
			c.Header("Retry-After", fmt.Sprintf("%d", int(l.RetryAfter(key).Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, rateLimitResponse())
			return
		}
		c.Next()
	}
}

// GinUsernameRateLimit returns middleware that limits login attempts by the
// username in the request body. It reads and restores the body so downstream
// handlers can still bind it. Requests with a missing/invalid body fall back
// to the IP dimension via the "unknown" bucket (they should fail validation anyway).
func GinUsernameRateLimit(l *Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewReader(body)) // restore for handler

		username := "unknown"
		if len(body) > 0 {
			var req struct {
				Username string `json:"username"`
			}
			if err := json.Unmarshal(body, &req); err == nil && req.Username != "" {
				username = req.Username
			}
		}

		key := "username:" + username
		if !l.Allow(key) {
			// Audit event (FR-014): rate-limit triggers are security events.
			slog.Warn("rate limit triggered", "dimension", "username", "key", username,
				"request_id", c.GetString("X-Request-Id"))
			c.Header("Retry-After", fmt.Sprintf("%d", int(l.RetryAfter(key).Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, rateLimitResponse())
			return
		}
		c.Next()
	}
}
