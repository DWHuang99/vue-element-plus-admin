package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
)

// Auth returns Gin middleware that validates a Bearer token and injects the
// authenticated user into the request context. Any failure returns a uniform
// 401 AUTH_INVALID_TOKEN response.
// Context keys live in the auth package: auth.ContextAuthUser / auth.ContextTokenHash.
func Auth(svc auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "缺少或格式无效的认证令牌",
				},
			})
			return
		}

		tokenHash := auth.HashToken(token)
		principal, err := svc.Authenticate(c.Request.Context(), tokenHash)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "认证令牌无效、已过期或已撤销",
				},
			})
			return
		}

		c.Set(auth.ContextAuthUser, principal)
		c.Set(auth.ContextTokenHash, tokenHash)
		c.Next()
	}
}

// BearerToken returns middleware that validates only the *format* of the
// Authorization header (no session validation). It injects auth.ContextTokenHash
// and proceeds. Used by logout so that revoking an already-invalid/revoked token
// stays idempotent (204), while a missing/malformed header still gets 401.
func BearerToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "缺少或格式无效的认证令牌",
				},
			})
			return
		}
		c.Set(auth.ContextTokenHash, auth.HashToken(token))
		c.Next()
	}
}

// bearerToken extracts the raw token from "Authorization: Bearer <token>".
func bearerToken(c *gin.Context) (string, bool) {
	header := c.GetHeader("Authorization")
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}
