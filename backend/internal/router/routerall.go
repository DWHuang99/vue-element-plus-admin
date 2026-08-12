package apirouter

import (
	db "vue-element-plus-admin/backend/internal/database/generated"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	"vue-element-plus-admin/backend/internal/modules/auth"
	"vue-element-plus-admin/backend/internal/modules/user"

	"github.com/redis/go-redis/v9"

	"github.com/gin-gonic/gin"
)

func AuthRouter(api *gin.RouterGroup, queries *db.Queries, jwtmanager *jwtservice.JWTManager, rdbClient *redis.Client, cookieSecure bool) {
	auth.RegisterAuthRoutes(
		api,
		auth.NewAuthHandler(
			auth.NewService(
				auth.NewRepository(queries),
				jwtmanager,
				rdbClient,
			),
			cookieSecure,
		),
	)
}

func UserRouter(api *gin.RouterGroup, queries *db.Queries, jwtmanager *jwtservice.JWTManager) {
	user.RegisterUserRoutes(
		api,
		user.NewUserHandler(
			user.NewService(
				user.NewRepository(queries),
			),
		),
		jwtmanager,
	)
}
