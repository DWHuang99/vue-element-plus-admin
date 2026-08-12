package main

import (
	"context"
	"vue-element-plus-admin/backend/internal/config"
	dbconnect "vue-element-plus-admin/backend/internal/database/connect"
	"vue-element-plus-admin/backend/internal/dto/response"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"
	"vue-element-plus-admin/backend/internal/modules/auth"

	"github.com/gin-gonic/gin"
)

func main() {
	ctx := context.Background()
	DbConfig := config.LoadDbConfig()
	jwtConfig := config.LoadJwtConfig()
	redisConfig := config.LoadRedisConfig()
	cookieConfig := config.LoadCookieConfig()

	queries, database := dbconnect.Connect(ctx, DbConfig)
	rdbClient := rdb.ConnectRedis(ctx, redisConfig)

	defer database.Close()
	defer rdbClient.Close()

	router := gin.Default()

	router.GET("/ping", func(c *gin.Context) {
		response.Success(c, gin.H{
			"message": "pong",
		})
	})

	api := router.Group("/api/v1")

	auth.RegisterAuthRoutes(
		api, auth.NewAuthHandler(
			auth.NewService(
				auth.NewRepository(queries),
				jwtservice.CreateJWTManager(jwtConfig),
				rdbClient,
			),
			cookieConfig.Secure,
		),
	)

	router.Run()
}
