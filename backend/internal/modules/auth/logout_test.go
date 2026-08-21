package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func newTestRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func newLogoutTestRouter(t *testing.T, redisClient *redis.Client) *gin.Engine {
	jwtmanager := newAuthTestJWTManager(t)
	service := &AuthService{
		jwtmanager:  jwtmanager,
		redisClient: redisClient,
	}
	router := gin.New()
	RegisterAuthRoutes(router.Group("/api/v1"), NewAuthHandler(service, false))
	return router
}

func newAuthTestJWTManager(t *testing.T) *jwtservice.JWTManager {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test RSA key: %v", err)
	}
	return jwtservice.NewJWTManager(
		privateKey,
		&privateKey.PublicKey,
		"test-key",
		"test-issuer",
		"test-api",
		time.Minute,
		time.Hour,
	)
}

func performLogout(router http.Handler, refreshToken string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	if refreshToken != "" {
		request.AddCookie(&http.Cookie{Name: "refresh_token", Value: refreshToken})
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func hasExpiredCookie(cookies []*http.Cookie, path string) bool {
	for _, cookie := range cookies {
		if cookie.Name == "refresh_token" && cookie.Path == path && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestRefreshCookieIsAvailableToLogout(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() error = %v", err)
	}
	refreshURL, _ := url.Parse("https://example.com/api/v1/auth/refresh")
	logoutURL, _ := url.Parse("https://example.com/api/v1/auth/logout")
	jar.SetCookies(refreshURL, []*http.Cookie{
		{Name: "refresh_token", Value: "token", Path: refreshCookiePath, Secure: true, HttpOnly: true},
	})

	cookies := jar.Cookies(logoutURL)
	if len(cookies) != 1 || cookies[0].Value != "token" {
		t.Fatalf("logout cookies = %#v, want refresh token", cookies)
	}
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	redisClient := newTestRedisClient(t)
	service := &AuthService{
		jwtmanager:  newAuthTestJWTManager(t),
		redisClient: redisClient,
	}
	refreshToken, err := rdb.CreateRefreshToken(redisClient, context.Background(), 1, time.Hour)
	if err != nil {
		t.Fatalf("CreateRefreshToken() error = %v", err)
	}

	if err := service.Logout(context.Background(), refreshToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	_, _, err = service.Refresh(context.Background(), refreshToken)
	if !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("Refresh() error = %v, want %v", err, ErrInvalidRefreshToken)
	}
}

func TestLogoutHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("revokes token and clears current and legacy cookies", func(t *testing.T) {
		redisClient := newTestRedisClient(t)
		refreshToken, err := rdb.CreateRefreshToken(redisClient, context.Background(), 1, time.Hour)
		if err != nil {
			t.Fatalf("CreateRefreshToken() error = %v", err)
		}
		recorder := performLogout(newLogoutTestRouter(t, redisClient), refreshToken)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		if _, _, err := rdb.RotateRefreshToken(redisClient, context.Background(), refreshToken, time.Hour); !errors.Is(err, rdb.ErrRefreshTokenNotFound) {
			t.Fatalf("refresh token still exists after logout, RotateRefreshToken() error = %v", err)
		}
		cookies := recorder.Result().Cookies()
		if !hasExpiredCookie(cookies, refreshCookiePath) {
			t.Fatalf("current-path cookie was not expired: %#v", cookies)
		}
		if !hasExpiredCookie(cookies, legacyRefreshCookiePath) {
			t.Fatalf("legacy-path cookie was not expired: %#v", cookies)
		}
	})

	t.Run("is idempotent without cookie", func(t *testing.T) {
		recorder := performLogout(newLogoutTestRouter(t, newTestRedisClient(t)), "")

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
	})

	t.Run("clears cookie when token store fails", func(t *testing.T) {
		redisClient := newTestRedisClient(t)
		// 关闭连接，强制 Delete 命令失败。
		redisClient.Close()
		recorder := performLogout(newLogoutTestRouter(t, redisClient), "old-token")

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if !hasExpiredCookie(recorder.Result().Cookies(), refreshCookiePath) {
			t.Fatal("cookie was not expired after token store failure")
		}
	})
}
