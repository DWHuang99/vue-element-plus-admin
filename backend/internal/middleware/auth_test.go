//go:build rollback

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// mockAuthSvc implements auth.Service for middleware tests.
type mockAuthSvc struct {
	principal *auth.AuthUser
	err       error
}

func (m *mockAuthSvc) Register(ctx context.Context, username, password string) (*auth.AuthResult, error) {
	return nil, auth.ErrInvalidInput
}
func (m *mockAuthSvc) Login(ctx context.Context, username, password string) (*auth.AuthResult, error) {
	return nil, auth.ErrInvalidInput
}
func (m *mockAuthSvc) Logout(ctx context.Context, tokenHash string) error { return nil }
func (m *mockAuthSvc) Authenticate(ctx context.Context, tokenHash string) (*auth.AuthUser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.principal, nil
}
func (m *mockAuthSvc) GetUserProfile(ctx context.Context, userID int64) (*auth.UserProfile, error) {
	return nil, auth.ErrInvalidToken
}

func newAuthRouter(svc auth.Service, handler func(c *gin.Context)) *gin.Engine {
	router := gin.New()
	router.GET("/protected", Auth(svc), handler)
	return router
}

func TestAuth_ValidTokenInjectsUser(t *testing.T) {
	svc := &mockAuthSvc{principal: &auth.AuthUser{ID: 5, Username: "alice"}}

	gotUser := false
	router := newAuthRouter(svc, func(c *gin.Context) {
		u, ok := c.Get(auth.ContextAuthUser)
		require.True(t, ok)
		principal := u.(*auth.AuthUser)
		assert.Equal(t, int64(5), principal.ID)
		assert.Equal(t, "alice", principal.Username)
		hash := c.GetString(auth.ContextTokenHash)
		assert.NotEmpty(t, hash)
		gotUser = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer some-raw-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, gotUser)
}

func TestAuth_MissingHeader(t *testing.T) {
	svc := &mockAuthSvc{principal: &auth.AuthUser{ID: 5, Username: "alice"}}
	router := newAuthRouter(svc, func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil) // no Authorization header
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "AUTH_INVALID_TOKEN")
	assert.NotContains(t, body, "goroutine", "no stack traces")
}

func TestAuth_MalformedHeader(t *testing.T) {
	svc := &mockAuthSvc{principal: &auth.AuthUser{ID: 5, Username: "alice"}}
	router := newAuthRouter(svc, func(c *gin.Context) { c.Status(http.StatusOK) })

	// "Basic xyz" — not Bearer.
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Basic xyz")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// "Bearer" with no token.
	req2 := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req2.Header.Set("Authorization", "Bearer ")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusUnauthorized, w2.Code)
}

func TestAuth_RevokedOrInvalidToken(t *testing.T) {
	svc := &mockAuthSvc{err: auth.ErrInvalidToken}
	router := newAuthRouter(svc, func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer revoked-or-expired-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	var errObj struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body["error"], &errObj))
	assert.Equal(t, "AUTH_INVALID_TOKEN", errObj.Code)
}

func TestBearerToken_ValidFormatProceeds(t *testing.T) {
	router := gin.New()
	router.POST("/logout", BearerToken(), func(c *gin.Context) {
		hash := c.GetString(auth.ContextTokenHash)
		assert.Equal(t, 64, len(hash), "token hash must be injected")
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer some-raw-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestBearerToken_MissingHeaderRejected(t *testing.T) {
	router := gin.New()
	router.POST("/logout", BearerToken(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodPost, "/logout", nil) // no header
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "AUTH_INVALID_TOKEN")
}

func TestAuth_ResponseNoSecrets(t *testing.T) {
	svc := &mockAuthSvc{err: auth.ErrInvalidToken}
	router := newAuthRouter(svc, func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer super-secret-token-abc")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	body := w.Body.String()
	assert.NotContains(t, body, "super-secret-token-abc", "response must not echo the raw token")
	assert.NotContains(t, body, "password")
	assert.NotContains(t, body, "internal/")
}
