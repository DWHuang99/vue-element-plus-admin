package apirouter

import (
	"crypto/rsa"
	"database/sql"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
	"vue-element-plus-admin/backend/internal/middleware/authorization"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	"vue-element-plus-admin/backend/internal/modules/auth"
	"vue-element-plus-admin/backend/internal/modules/menu"
	"vue-element-plus-admin/backend/internal/modules/role"
	"vue-element-plus-admin/backend/internal/modules/user"
	"vue-element-plus-admin/backend/internal/modules/usermanagement"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
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

func UserRouter(
	api *gin.RouterGroup,
	queries *db.Queries,
	jwtmanager *jwtservice.JWTManager,
	managementService *usermanagement.Service,
) {
	repository := user.NewRepository(queries)
	user.RegisterUserRoutes(
		api,
		user.NewUserHandler(
			user.NewService(
				repository,
			),
		),
		jwtmanager,
	)
	usermanagement.RegisterRoutes(
		api,
		usermanagement.NewHandler(managementService),
		authorization.NewDatabaseChecker(queries),
		jwtmanager,
	)
}

func MenuRouter(api *gin.RouterGroup, queries *db.Queries, jwtmanager *jwtservice.JWTManager) {
	menu.RegisterRoutes(
		api,
		menu.NewHandler(menu.NewService(menu.NewRepository(queries))),
		authorization.NewDatabaseChecker(queries),
		jwtmanager,
	)
}

func RoleRouter(api *gin.RouterGroup, queries *db.Queries, database *sql.DB, jwtmanager *jwtservice.JWTManager) {
	role.RegisterRoutes(
		api,
		role.NewHandler(role.NewService(role.NewRepository(database, queries))),
		authorization.NewDatabaseChecker(queries),
		jwtmanager,
	)
}

func PublicKeyRouter(router gin.IRoutes, publicKey *rsa.PublicKey, keyID string) {
	jwtservice.RegisterJWKSRoutes(router, jwtservice.NewJWKSHandler(publicKey, keyID))
}
