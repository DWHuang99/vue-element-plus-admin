package menu

import (
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/casbin/casbin/v3"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(api *gin.RouterGroup, handler *Handler, enforcer *casbin.SyncedEnforcer, jwtManager *jwtservice.JWTManager) {
	menus := api.Group("/menus")
	menus.Use(jwtservice.JwtFilter(jwtManager))
	menus.GET("/tree", authorization.RequirePermission(enforcer, authorization.MenuRead), handler.MenuTree)
	menus.POST("", authorization.RequirePermission(enforcer, authorization.MenuCreate), handler.MenuCreate)
	menus.PUT("/:id", authorization.RequirePermission(enforcer, authorization.MenuUpdate), handler.MenuUpdate)
	menus.DELETE("/:id", authorization.RequirePermission(enforcer, authorization.MenuDelete), handler.MenuDelete)
}
