package user

import (
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

func RegisterUserRoutes(router *gin.RouterGroup, handler *UserHandler, jwtManager *jwtservice.JWTManager) {
	users := router.Group("/users")
	users.Use(jwtservice.JwtFilter(jwtManager))
	users.GET("/me", handler.GetCurrentUser)
}
