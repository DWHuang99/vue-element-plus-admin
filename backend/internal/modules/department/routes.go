package department

import (
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(api *gin.RouterGroup, handler *Handler, jwtManager *jwtservice.JWTManager) {
	departments := api.Group("/departments")
	departments.Use(jwtservice.JwtFilter(jwtManager))
	departments.GET("/tree", authorization.RequireTokenPermission(authorization.DepartmentRead), handler.DepartmentTree)
	departments.GET("", authorization.RequireTokenPermission(authorization.DepartmentRead), handler.DepartmentList)
	departments.POST("", authorization.RequireTokenPermission(authorization.DepartmentCreate), handler.DepartmentCreate)
	departments.PUT("/:id", authorization.RequireTokenPermission(authorization.DepartmentUpdate), handler.DepartmentUpdate)
	departments.DELETE("", authorization.RequireTokenPermission(authorization.DepartmentDelete), handler.DepartmentDelete)
}
