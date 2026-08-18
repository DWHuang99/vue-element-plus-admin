package usermanagement

import (
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/casbin/casbin/v3"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(
	router *gin.RouterGroup,
	handler *Handler,
	enforcer *casbin.SyncedEnforcer,
	jwtManager *jwtservice.JWTManager,
) {
	users := router.Group("/users")
	users.Use(jwtservice.JwtFilter(jwtManager))
	users.GET("", authorization.RequirePermission(enforcer, authorization.UserRead), handler.UserList)
	users.POST("", authorization.RequirePermission(enforcer, authorization.UserCreate), handler.UserCreate)
	users.PUT("/:id", authorization.RequirePermission(enforcer, authorization.UserUpdate), handler.UserUpdate)
	users.DELETE("", authorization.RequirePermission(enforcer, authorization.UserDelete), handler.UserDelete)
}
