//go:build rollback

package rbac

import (
	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/authorization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
)

// RegisterRoutes binds the nine management endpoints to their required
// read/write permissions. Authentication is applied by the caller's group.
func RegisterRoutes(group *gin.RouterGroup, handler *Handler, authorizationSvc authorization.Service) {
	group.GET("/roles", middleware.RequirePermission(authorizationSvc, authorization.RolesRead), handler.ListRoles)
	group.POST("/roles", middleware.RequirePermission(authorizationSvc, authorization.RolesWrite), handler.SaveRole)
	group.POST("/roles/delete", middleware.RequirePermission(authorizationSvc, authorization.RolesWrite), handler.DeleteRoles)

	group.GET("/departments", middleware.RequirePermission(authorizationSvc, authorization.DepartmentsRead), handler.ListDepartments)
	group.POST("/departments", middleware.RequirePermission(authorizationSvc, authorization.DepartmentsWrite), handler.SaveDepartment)
	group.POST("/departments/delete", middleware.RequirePermission(authorizationSvc, authorization.DepartmentsWrite), handler.DeleteDepartments)

	group.GET("/users", middleware.RequirePermission(authorizationSvc, authorization.UsersRead), handler.ListUsers)
	group.POST("/users", middleware.RequirePermission(authorizationSvc, authorization.UsersWrite), handler.SaveUser)
	group.POST("/users/delete", middleware.RequirePermission(authorizationSvc, authorization.UsersWrite), handler.DeleteUsers)
}
