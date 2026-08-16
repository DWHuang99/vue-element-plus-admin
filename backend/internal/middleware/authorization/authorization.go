package authorization

import (
	"context"
	"net/http"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
	"vue-element-plus-admin/backend/internal/dto/response"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

const (
	UserRead         = "system:user:read"
	UserCreate       = "system:user:create"
	UserUpdate       = "system:user:update"
	UserDelete       = "system:user:delete"
	RoleRead         = "system:role:read"
	RoleCreate       = "system:role:create"
	RoleUpdate       = "system:role:update"
	RoleDelete       = "system:role:delete"
	MenuRead         = "system:menu:read"
	MenuCreate       = "system:menu:create"
	MenuUpdate       = "system:menu:update"
	MenuDelete       = "system:menu:delete"
	DepartmentRead   = "system:department:read"
	DepartmentCreate = "system:department:create"
	DepartmentUpdate = "system:department:update"
	DepartmentDelete = "system:department:delete"
)

type PermissionChecker interface {
	HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error)
}

type DatabaseChecker struct {
	queries *db.Queries
}

func NewDatabaseChecker(queries *db.Queries) *DatabaseChecker {
	return &DatabaseChecker{queries: queries}
}

func (c *DatabaseChecker) HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error) {
	return c.queries.HasUserPermission(ctx, db.HasUserPermissionParams{
		UserID:         userID,
		PermissionCode: permissionCode,
	})
}

func RequirePermission(checker PermissionChecker, permissionCode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDValue, exists := c.Get(jwtservice.UserIDContextKey)
		userID, ok := userIDValue.(int64)
		if !exists || !ok || userID <= 0 {
			response.Error(c, http.StatusUnauthorized, 401, "user identity not found")
			c.Abort()
			return
		}

		allowed, err := checker.HasPermission(c.Request.Context(), userID, permissionCode)
		if err != nil {
			response.Error(c, http.StatusInternalServerError, 10500, "permission check failed")
			c.Abort()
			return
		}
		if !allowed {
			response.Error(c, http.StatusForbidden, 40301, "permission denied")
			c.Abort()
			return
		}

		c.Next()
	}
}

// RequireTokenPermission is used by resource services that do not own the IAM
// database. Permissions are signed into the short-lived access token by IAM.
func RequireTokenPermission(permissionCode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		value, exists := c.Get(jwtservice.PermissionsContextKey)
		permissions, ok := value.([]string)
		if !exists || !ok {
			response.Error(c, http.StatusForbidden, 40301, "permission denied")
			c.Abort()
			return
		}

		for _, candidate := range permissions {
			if candidate == permissionCode || candidate == "*" || candidate == "*:*:*" || candidate == "*.*.*" {
				c.Next()
				return
			}
		}

		response.Error(c, http.StatusForbidden, 40301, "permission denied")
		c.Abort()
	}
}
