package gmailapi

import (
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

func GmailRouter(router *gin.RouterGroup, handler *GmailHandler, jwtManager *jwtservice.JWTManager) {
	gmail := router.Group("/gmail")
	gmail.Use(jwtservice.JwtFilter(jwtManager))
	gmail.GET("/unread", handler.ListUnreadMessages)
}
