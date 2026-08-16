package menu

import (
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(api *gin.RouterGroup, handler *Handler, checker authorization.PermissionChecker, jwtManager *jwtservice.JWTManager) {
	menus := api.Group("/menus")
	menus.Use(jwtservice.JwtFilter(jwtManager))
	menus.GET("/tree", authorization.RequirePermission(checker, authorization.MenuRead), handler.MenuTree)
	menus.POST("", authorization.RequirePermission(checker, authorization.MenuCreate), handler.MenuCreate)
	menus.PUT("/:id", authorization.RequirePermission(checker, authorization.MenuUpdate), handler.MenuUpdate)
	menus.DELETE("/:id", authorization.RequirePermission(checker, authorization.MenuDelete), handler.MenuDelete)
}
