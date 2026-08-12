package main

import (
	"context"
	"vue-element-plus-admin/backend/internal/config"
	dbconnect "vue-element-plus-admin/backend/internal/database/connect"
	"vue-element-plus-admin/backend/internal/dto/response"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"
	apirouter "vue-element-plus-admin/backend/internal/router"

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
	jwtmanager := jwtservice.CreateJWTManager(jwtConfig)

	defer database.Close()
	defer rdbClient.Close()

	router := gin.Default()
	api := router.Group("/api/v1")

	router.GET("/ping", func(c *gin.Context) {
		response.Success(c, gin.H{
			"message": "pong",
		})
	})

	apirouter.AuthRouter(
		api, queries, jwtmanager, rdbClient, cookieConfig.Secure,
	)
	apirouter.UserRouter(
		api, queries, jwtmanager,
	)

	router.Run()
}
