//go:build rollback

package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/authorization"
)

// RequirePermission allows a request only when the authenticated principal has
// the requested permission in the current database state. It must run after Auth.
func RequirePermission(svc authorization.Service, permissionCode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		value, ok := c.Get(auth.ContextAuthUser)
		principal, valid := value.(*auth.AuthUser)
		if !ok || !valid || principal == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "缺少或无效的认证用户",
				},
			})
			return
		}

		allowed, err := svc.HasPermission(c.Request.Context(), principal.ID, permissionCode)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"code":    "INTERNAL_ERROR",
					"message": "内部错误",
				},
			})
			return
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"code":    "AUTH_FORBIDDEN",
					"message": "无权执行此操作",
				},
			})
			return
		}

		c.Next()
	}
}
