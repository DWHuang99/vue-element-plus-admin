package usermanagement

import (
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(
	router *gin.RouterGroup,
	handler *Handler,
	checker authorization.PermissionChecker,
	jwtManager *jwtservice.JWTManager,
) {
	users := router.Group("/users")
	users.Use(jwtservice.JwtFilter(jwtManager))
	users.GET("", authorization.RequirePermission(checker, authorization.UserRead), handler.UserList)
	users.POST("", authorization.RequirePermission(checker, authorization.UserCreate), handler.UserCreate)
	users.PUT("/:id", authorization.RequirePermission(checker, authorization.UserUpdate), handler.UserUpdate)
	users.DELETE("", authorization.RequirePermission(checker, authorization.UserDelete), handler.UserDelete)
}
