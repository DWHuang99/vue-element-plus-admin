package main

import (
	"context"
	"log"

	"vue-element-plus-admin/backend/internal/config"
	dbconnect "vue-element-plus-admin/backend/internal/database/connect"
	departmentdb "vue-element-plus-admin/backend/internal/database/department/generated"
	"vue-element-plus-admin/backend/internal/dto/response"
	departmentgrpc "vue-element-plus-admin/backend/internal/grpc/department"
	usermanagementgrpc "vue-element-plus-admin/backend/internal/grpc/usermanagement"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	"vue-element-plus-admin/backend/internal/modules/department"
	"vue-element-plus-admin/backend/pb"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, err := dbconnect.Connect(ctx, config.LoadDbConfig())
	if err != nil {
		log.Fatalf("connect Department database: %v", err)
	}
	defer database.Close()

	verifierConfig := config.LoadJWTVerifierConfig()
	keyfunc, err := jwtservice.NewRemoteJWKSKeyfunc(ctx, verifierConfig.JWKSURL)
	if err != nil {
		log.Fatalf("initialize JWKS verifier: %v", err)
	}
	jwtmanager := jwtservice.NewJWTVerifier(
		keyfunc,
		verifierConfig.Issuer,
		verifierConfig.Audience,
	)
	serviceConfig, err := config.LoadServiceConfig()
	if err != nil {
		log.Fatalf("load service configuration: %v", err)
	}

	queries := departmentdb.New(database)
	iamConnection, err := grpc.NewClient(
		serviceConfig.IAMGRPCTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("create IAM gRPC client: %v", err)
	}
	defer iamConnection.Close()
	userDirectory := usermanagementgrpc.NewDirectory(
		pb.NewUserManagementServiceClient(iamConnection),
		serviceConfig.GRPCTimeout,
	)
	service := department.NewService(department.NewRepository(queries), userDirectory)
	handler := department.NewHandler(service)
	go func() {
		if err := departmentgrpc.Serve(serviceConfig.DepartmentGRPCAddr, service); err != nil {
			log.Fatalf("run Department gRPC service: %v", err)
		}
	}()
	router := gin.Default()
	api := router.Group("/api/v1")
	department.RegisterRoutes(api, handler, jwtmanager)
	router.GET("/ping", func(c *gin.Context) {
		response.Success(c, gin.H{"message": "pong"})
	})

	if err := router.Run(); err != nil {
		log.Fatalf("run Department service: %v", err)
	}
}
