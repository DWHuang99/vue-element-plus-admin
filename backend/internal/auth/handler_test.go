package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// mockService is a test double for the Service interface.
type mockService struct {
	registerFn     func(ctx context.Context, username, password string) (*AuthResult, error)
	loginFn        func(ctx context.Context, username, password string) (*AuthResult, error)
	logoutFn       func(ctx context.Context, tokenHash string) error
	authenticateFn func(ctx context.Context, tokenHash string) (*AuthUser, error)
}

func (m *mockService) Register(ctx context.Context, username, password string) (*AuthResult, error) {
	if m.registerFn != nil {
		return m.registerFn(ctx, username, password)
	}
	return nil, ErrInvalidInput
}

func (m *mockService) Login(ctx context.Context, username, password string) (*AuthResult, error) {
	if m.loginFn != nil {
		return m.loginFn(ctx, username, password)
	}
	return nil, ErrInvalidInput
}

func (m *mockService) Logout(ctx context.Context, tokenHash string) error {
	if m.logoutFn != nil {
		return m.logoutFn(ctx, tokenHash)
	}
	return nil
}

func (m *mockService) Authenticate(ctx context.Context, tokenHash string) (*AuthUser, error) {
	if m.authenticateFn != nil {
		return m.authenticateFn(ctx, tokenHash)
	}
	return nil, ErrInvalidToken
}

func newTestHandler(m *mockService) *Handler {
	return NewHandler(m, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func doJSON(t *testing.T, h *Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.POST("/register", h.Register)
	router.POST("/login", h.Login)
	router.POST("/logout", h.Logout)
	router.GET("/me", h.Me)

	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}

	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) (map[string]json.RawMessage, errorEnvelope) {
	t.Helper()
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	var env errorEnvelope
	if raw, ok := body["error"]; ok {
		_ = json.Unmarshal(raw, &env.Error)
	}
	return body, env
}

func sampleResult(username string) *AuthResult {
	return &AuthResult{
		Token:     "sample-raw-token",
		TokenType: "Bearer",
		ExpiresIn: 86400,
		User:      UserView{ID: 1, Username: username, CreatedAt: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)},
	}
}

// T016: Register contract tests.
func TestHandler_RegisterSuccess(t *testing.T) {
	h := newTestHandler(&mockService{
		registerFn: func(_ context.Context, username, password string) (*AuthResult, error) {
			return sampleResult(username), nil
		},
	})

	w := doJSON(t, h, "POST", "/register", credentialsRequest{Username: "alice", Password: "supersecret123"}, nil)
	assert.Equal(t, http.StatusCreated, w.Code)

	body, _ := decodeEnvelope(t, w)
	var data struct {
		Token string `json:"token"`
		User  struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(body["data"], &data))
	assert.Equal(t, "sample-raw-token", data.Token)
	assert.Equal(t, "alice", data.User.Username)
}

func TestHandler_RegisterDuplicateUsername(t *testing.T) {
	h := newTestHandler(&mockService{
		registerFn: func(_ context.Context, _, _ string) (*AuthResult, error) {
			return nil, ErrUsernameTaken
		},
	})

	w := doJSON(t, h, "POST", "/register", credentialsRequest{Username: "alice", Password: "supersecret123"}, nil)
	assert.Equal(t, http.StatusConflict, w.Code)

	_, env := decodeEnvelope(t, w)
	assert.Equal(t, "AUTH_USERNAME_TAKEN", env.Error.Code)
}

func TestHandler_RegisterInvalidUsernameFormat(t *testing.T) {
	h := newTestHandler(&mockService{})

	cases := []struct {
		name     string
		username string
		field    string
	}{
		{"too_short", "ab", "username"},
		{"too_long", strings.Repeat("a", 33), "username"},
		{"invalid_chars", "alice@x", "username"},
		{"blank", "   ", "username"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, h, "POST", "/register", credentialsRequest{Username: tc.username, Password: "supersecret123"}, nil)
			assert.Equal(t, http.StatusBadRequest, w.Code)

			_, env := decodeEnvelope(t, w)
			assert.Equal(t, "AUTH_INVALID_INPUT", env.Error.Code)
			require.Len(t, env.Error.FieldErrors, 1)
			assert.Equal(t, tc.field, env.Error.FieldErrors[0].Field)
		})
	}
}

func TestHandler_RegisterInvalidPassword(t *testing.T) {
	h := newTestHandler(&mockService{})

	cases := []struct {
		name     string
		password string
	}{
		{"too_short", "1234567"}, // 7 chars
		{"blank", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, h, "POST", "/register", credentialsRequest{Username: "alice", Password: tc.password}, nil)
			assert.Equal(t, http.StatusBadRequest, w.Code)

			_, env := decodeEnvelope(t, w)
			assert.Equal(t, "AUTH_INVALID_INPUT", env.Error.Code)
			assert.Equal(t, "password", env.Error.FieldErrors[0].Field)
		})
	}
}

func TestHandler_RegisterNoSecretsInResponse(t *testing.T) {
	h := newTestHandler(&mockService{
		registerFn: func(_ context.Context, _, _ string) (*AuthResult, error) {
			return nil, ErrUsernameTaken
		},
	})

	w := doJSON(t, h, "POST", "/register", credentialsRequest{Username: "alice", Password: "supersecret123"}, nil)
	bodyStr := w.Body.String()

	assert.NotContains(t, bodyStr, "supersecret123", "response must not contain the password")
	assert.NotContains(t, bodyStr, "alice@", "no internal paths")
	assert.NotContains(t, bodyStr, "goroutine", "no stack traces")
}

// T024: Login contract tests.
func TestHandler_LoginSuccess(t *testing.T) {
	h := newTestHandler(&mockService{
		loginFn: func(_ context.Context, username, _ string) (*AuthResult, error) {
			return sampleResult(username), nil
		},
	})

	w := doJSON(t, h, "POST", "/login", credentialsRequest{Username: "alice", Password: "supersecret123"}, nil)
	assert.Equal(t, http.StatusOK, w.Code)
	_, env := decodeEnvelope(t, w)
	assert.Empty(t, env.Error.Code, "no error envelope on success")
}

func TestHandler_LoginFailureUniform(t *testing.T) {
	// Wrong password and nonexistent user both yield identical 401 envelopes.
	results := map[string]int{}
	for _, tc := range []struct {
		name     string
		username string
		password string
	}{
		{"wrong_password", "alice", "wrongpass123"},
		{"nonexistent_user", "ghost", "wrongpass123"},
	} {
		h := newTestHandler(&mockService{
			loginFn: func(_ context.Context, _, _ string) (*AuthResult, error) {
				return nil, ErrInvalidCredentials
			},
		})
		w := doJSON(t, h, "POST", "/login", credentialsRequest{Username: tc.username, Password: tc.password}, nil)
		assert.Equal(t, http.StatusUnauthorized, w.Code)

		_, env := decodeEnvelope(t, w)
		assert.Equal(t, "AUTH_INVALID_CREDENTIALS", env.Error.Code)
		results[tc.name] = len(w.Body.String())
	}
	assert.Equal(t, results["wrong_password"], results["nonexistent_user"],
		"both login failures must produce byte-length-identical envelopes")
}

func TestHandler_LoginInvalidInput(t *testing.T) {
	h := newTestHandler(&mockService{})
	w := doJSON(t, h, "POST", "/login", credentialsRequest{Username: "ab", Password: "short"}, nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	_, env := decodeEnvelope(t, w)
	assert.Equal(t, "AUTH_INVALID_INPUT", env.Error.Code)
	require.Len(t, env.Error.FieldErrors, 2)
}

// T029: Logout + me contract tests.
func TestHandler_Logout(t *testing.T) {
	loggedOut := false
	h := newTestHandler(&mockService{
		logoutFn: func(_ context.Context, _ string) error {
			loggedOut = true
			return nil
		},
	})

	// The handler relies on middleware to set auth_token_hash; simulate it here.
	router := gin.New()
	router.POST("/logout", func(c *gin.Context) {
		c.Set(ContextTokenHash, "abcdef")
		h.Logout(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.True(t, loggedOut, "service.Logout must be called")
}

func TestHandler_LogoutIdempotent(t *testing.T) {
	// Contract: revoking an already-invalid/revoked token is idempotent (204).
	h := newTestHandler(&mockService{
		logoutFn: func(_ context.Context, _ string) error {
			return nil // service returns nil even when token is unknown/revoked
		},
	})

	// Simulate BearerToken middleware setting the token hash (well-formed token).
	router := gin.New()
	router.POST("/logout", func(c *gin.Context) {
		c.Set(ContextTokenHash, authHashOf("revoked-or-invalid-token"))
		h.Logout(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer revoked-or-invalid-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code, "logout must be idempotent for invalid-but-wellformed tokens")
}

func authHashOf(token string) string {
	return HashToken(token)
}

func TestHandler_LogoutMissingHashReturns401(t *testing.T) {
	h := newTestHandler(&mockService{})
	w := doJSON(t, h, "POST", "/logout", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	_, env := decodeEnvelope(t, w)
	assert.Equal(t, "AUTH_INVALID_TOKEN", env.Error.Code)
}

func TestHandler_Me(t *testing.T) {
	h := newTestHandler(&mockService{})

	router := gin.New()
	router.GET("/me", func(c *gin.Context) {
		c.Set(ContextAuthUser, &AuthUser{ID: 7, Username: "alice"})
		h.Me(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	var data struct {
		User struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(body["data"], &data))
	assert.Equal(t, int64(7), data.User.ID)
	assert.Equal(t, "alice", data.User.Username)
}

func TestHandler_MeMissingPrincipalReturns401(t *testing.T) {
	h := newTestHandler(&mockService{})
	w := doJSON(t, h, "GET", "/me", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	_, env := decodeEnvelope(t, w)
	assert.Equal(t, "AUTH_INVALID_TOKEN", env.Error.Code)
}
