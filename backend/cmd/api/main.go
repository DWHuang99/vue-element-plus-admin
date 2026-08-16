package main

import (
	"context"
	"log"

	"vue-element-plus-admin/backend/internal/config"
	dbconnect "vue-element-plus-admin/backend/internal/database/connect"
	iamdb "vue-element-plus-admin/backend/internal/database/iam/generated"
	"vue-element-plus-admin/backend/internal/dto/response"
	departmentgrpc "vue-element-plus-admin/backend/internal/grpc/department"
	usermanagementgrpc "vue-element-plus-admin/backend/internal/grpc/usermanagement"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"
	"vue-element-plus-admin/backend/internal/modules/usermanagement"
	apirouter "vue-element-plus-admin/backend/internal/router"
	"vue-element-plus-admin/backend/pb"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	ctx := context.Background()
	DbConfig := config.LoadDbConfig()
	jwtConfig, err := config.LoadJwtConfig()
	if err != nil {
		log.Fatalf("load JWT configuration: %v", err)
	}
	redisConfig := config.LoadRedisConfig()
	cookieConfig := config.LoadCookieConfig()
	serviceConfig, err := config.LoadServiceConfig()
	if err != nil {
		log.Fatalf("load service configuration: %v", err)
	}

	database, err := dbconnect.Connect(ctx, DbConfig)
	if err != nil {
		log.Fatalf("connect IAM database: %v", err)
	}
	queries := iamdb.New(database)
	rdbClient := rdb.ConnectRedis(ctx, redisConfig)
	jwtmanager := jwtservice.CreateJWTManager(jwtConfig)
	departmentConnection, err := grpc.NewClient(
		serviceConfig.DepartmentGRPCTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("create Department gRPC client: %v", err)
	}
	defer departmentConnection.Close()
	departmentDirectory := departmentgrpc.NewDirectory(
		pb.NewDepartmentServiceClient(departmentConnection),
		serviceConfig.GRPCTimeout,
	)
	managementService := usermanagement.NewService(
		usermanagement.NewRepository(queries),
		departmentDirectory,
	)
	go func() {
		if err := usermanagementgrpc.Serve(serviceConfig.IAMGRPCAddr, managementService); err != nil {
			log.Fatalf("run IAM gRPC service: %v", err)
		}
	}()

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
	apirouter.PublicKeyRouter(
		router, jwtConfig.PublicKey, jwtConfig.KeyID,
	)
	apirouter.UserRouter(
		api, queries, jwtmanager, managementService,
	)
	apirouter.MenuRouter(api, queries, jwtmanager)
	apirouter.RoleRouter(api, queries, database, jwtmanager)

	router.Run()
}
