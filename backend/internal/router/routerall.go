package apirouter

import (
	"crypto/rsa"
	"database/sql"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	"vue-element-plus-admin/backend/internal/modules/auth"
	"vue-element-plus-admin/backend/internal/modules/menu"
	"vue-element-plus-admin/backend/internal/modules/role"
	"vue-element-plus-admin/backend/internal/modules/user"
	"vue-element-plus-admin/backend/internal/modules/usermanagement"

	"github.com/casbin/casbin/v3"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func AuthRouter(
	api *gin.RouterGroup,
	queries *db.Queries,
	jwtmanager *jwtservice.JWTManager,
	rdbClient *redis.Client,
	cookieSecure bool,
	casbinEnforcer *casbin.SyncedEnforcer,
) {
	auth.RegisterAuthRoutes(
		api,
		auth.NewAuthHandler(
			auth.NewService(
				auth.NewRepository(queries),
				jwtmanager,
				rdbClient,
				casbinEnforcer,
			),
			cookieSecure,
		),
	)
}

func UserRouter(
	api *gin.RouterGroup,
	database *sql.DB,
	queries *db.Queries,
	jwtmanager *jwtservice.JWTManager,
	managementService *usermanagement.Service,
	casbinEnforcer *casbin.SyncedEnforcer,
) {
	repository := user.NewRepository(queries)
	menuService := menu.NewService(menu.NewRepository(database, queries))
	user.RegisterUserRoutes(
		api,
		user.NewUserHandler(
			user.NewService(
				repository,
				casbinEnforcer,
			),
			menuService,
		),
		jwtmanager,
	)
	usermanagement.RegisterRoutes(
		api,
		usermanagement.NewHandler(managementService),
		casbinEnforcer,
		jwtmanager,
	)
}

func MenuRouter(
	api *gin.RouterGroup,
	database *sql.DB,
	queries *db.Queries,
	jwtmanager *jwtservice.JWTManager,
	casbinEnforcer *casbin.SyncedEnforcer,
) {
	menu.RegisterRoutes(
		api,
		menu.NewHandler(menu.NewService(menu.NewRepository(database, queries))),
		casbinEnforcer,
		jwtmanager,
	)
}

func RoleRouter(
	api *gin.RouterGroup,
	queries *db.Queries,
	database *sql.DB,
	jwtmanager *jwtservice.JWTManager,
	casbinEnforcer *casbin.SyncedEnforcer,
) {
	role.RegisterRoutes(
		api,
		role.NewHandler(role.NewService(role.NewRepository(database, queries), casbinEnforcer)),
		casbinEnforcer,
		jwtmanager,
	)
}

func PublicKeyRouter(router gin.IRoutes, publicKey *rsa.PublicKey, keyID string) {
	jwtservice.RegisterJWKSRoutes(router, jwtservice.NewJWKSHandler(publicKey, keyID))
}
