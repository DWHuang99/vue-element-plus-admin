package role

import (
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/casbin/casbin/v3"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(api *gin.RouterGroup, handler *Handler, enforcer *casbin.SyncedEnforcer, jwtManager *jwtservice.JWTManager) {
	roles := api.Group("/roles")
	roles.Use(jwtservice.JwtFilter(jwtManager))
	roles.GET("", authorization.RequirePermission(enforcer, authorization.RoleRead), handler.RoleList)
	roles.GET("/:id", authorization.RequirePermission(enforcer, authorization.RoleRead), handler.RoleDetail)
	roles.POST("", authorization.RequirePermission(enforcer, authorization.RoleCreate), handler.RoleCreate)
	roles.PUT("/:id", authorization.RequirePermission(enforcer, authorization.RoleUpdate), handler.RoleUpdate)
	roles.DELETE("/:id", authorization.RequirePermission(enforcer, authorization.RoleDelete), handler.RoleDelete)
}
