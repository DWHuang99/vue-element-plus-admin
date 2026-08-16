package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"

	"github.com/gin-gonic/gin"
)

type memoryRefreshTokenStore struct {
	tokens      map[string]int64
	deleteErr   error
	deleteCalls int
	nextToken   int
}

func newMemoryRefreshTokenStore() *memoryRefreshTokenStore {
	return &memoryRefreshTokenStore{tokens: make(map[string]int64)}
}

func (s *memoryRefreshTokenStore) Create(_ context.Context, userID int64, _ time.Duration) (string, error) {
	s.nextToken++
	token := fmt.Sprintf("token-%d", s.nextToken)
	s.tokens[token] = userID
	return token, nil
}

func (s *memoryRefreshTokenStore) Rotate(_ context.Context, refreshToken string, _ time.Duration) (string, int64, error) {
	userID, ok := s.tokens[refreshToken]
	if !ok {
		return "", 0, rdb.ErrRefreshTokenNotFound
	}
	delete(s.tokens, refreshToken)
	s.nextToken++
	newToken := fmt.Sprintf("token-%d", s.nextToken)
	s.tokens[newToken] = userID
	return newToken, userID, nil
}

func (s *memoryRefreshTokenStore) Delete(_ context.Context, refreshToken string) error {
	s.deleteCalls++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.tokens, refreshToken)
	return nil
}

func newLogoutTestRouter(t *testing.T, store RefreshTokenStore) *gin.Engine {
	jwtmanager := newAuthTestJWTManager(t)
	service := &AuthService{
		jwtmanager:    jwtmanager,
		refreshTokens: store,
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
	store := newMemoryRefreshTokenStore()
	store.tokens["old-token"] = 1
	service := &AuthService{
		jwtmanager:    newAuthTestJWTManager(t),
		refreshTokens: store,
	}

	if err := service.Logout(context.Background(), "old-token"); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	_, _, err := service.Refresh(context.Background(), "old-token")
	if !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("Refresh() error = %v, want %v", err, ErrInvalidRefreshToken)
	}
}

func TestLogoutHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("revokes token and clears current and legacy cookies", func(t *testing.T) {
		store := newMemoryRefreshTokenStore()
		store.tokens["old-token"] = 1
		recorder := performLogout(newLogoutTestRouter(t, store), "old-token")

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		if _, exists := store.tokens["old-token"]; exists {
			t.Fatal("refresh token still exists after logout")
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
		store := newMemoryRefreshTokenStore()
		recorder := performLogout(newLogoutTestRouter(t, store), "")

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		if store.deleteCalls != 0 {
			t.Fatalf("Delete() calls = %d, want 0", store.deleteCalls)
		}
	})

	t.Run("clears cookie when token store fails", func(t *testing.T) {
		store := newMemoryRefreshTokenStore()
		store.deleteErr = errors.New("redis unavailable")
		recorder := performLogout(newLogoutTestRouter(t, store), "old-token")

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if !hasExpiredCookie(recorder.Result().Cookies(), refreshCookiePath) {
			t.Fatal("cookie was not expired after token store failure")
		}
	})
}
