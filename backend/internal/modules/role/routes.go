package role

import (
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(api *gin.RouterGroup, handler *Handler, checker authorization.PermissionChecker, jwtManager *jwtservice.JWTManager) {
	roles := api.Group("/roles")
	roles.Use(jwtservice.JwtFilter(jwtManager))
	roles.GET("", authorization.RequirePermission(checker, authorization.RoleRead), handler.RoleList)
	roles.GET("/:id", authorization.RequirePermission(checker, authorization.RoleRead), handler.RoleDetail)
	roles.POST("", authorization.RequirePermission(checker, authorization.RoleCreate), handler.RoleCreate)
	roles.PUT("/:id", authorization.RequirePermission(checker, authorization.RoleUpdate), handler.RoleUpdate)
	roles.DELETE("/:id", authorization.RequirePermission(checker, authorization.RoleDelete), handler.RoleDelete)
}
