package oauth

import (
	"github.com/gin-gonic/gin"
)

func OidcRegisterRoutes(api *gin.RouterGroup, handler *OauthHandler) {
	api.GET("/oauth/login", handler.Login)
	api.GET("/oauth/callback", handler.Callback)
}
