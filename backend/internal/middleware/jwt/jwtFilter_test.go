package jwtservice

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestJwtFilterUsesImmutableNumericSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := newTestJWTManager(t)

	router := gin.New()
	router.GET("/protected", JwtFilter(manager), func(c *gin.Context) {
		userID, exists := c.Get(UserIDContextKey)
		if !exists || userID != int64(7) {
			t.Fatalf("user ID = %#v, exists = %v", userID, exists)
		}
		c.Status(http.StatusNoContent)
	})

	t.Run("accepts a numeric user ID", func(t *testing.T) {
		token, err := manager.GenerateToken(7, []string{"admin"})
		if err != nil {
			t.Fatalf("GenerateToken() error = %v", err)
		}
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	})

	t.Run("rejects a legacy username subject", func(t *testing.T) {
		now := time.Now()
		claims := Claims{
			Role: []string{"admin"},
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    testIssuer,
				Subject:   "admin",
				Audience:  jwt.ClaimStrings{testAudience},
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			},
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
		if err != nil {
			t.Fatalf("sign token: %v", err)
		}
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
		}
	})
}
