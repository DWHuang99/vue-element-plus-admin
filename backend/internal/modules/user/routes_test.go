package user

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

func TestCurrentUserRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	jwtManager := jwtservice.NewJWTManager(
		privateKey,
		&privateKey.PublicKey,
		"test-key",
		"test-issuer",
		"test-api",
		time.Minute,
		time.Hour,
	)
	token, err := jwtManager.GenerateToken(1, []string{"admin"})
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	service := &currentUserServiceStub{user: &CurrentUser{Username: "admin", IsActive: true}}
	router := gin.New()
	RegisterUserRoutes(router.Group("/api/v1"), NewUserHandler(service, nil), jwtManager)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if service.userID != 1 {
		t.Fatalf("service user ID = %d, want 1", service.userID)
	}
}
