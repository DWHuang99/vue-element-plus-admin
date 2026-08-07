package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func newGinRouter(l *Limiter, userLimiter *Limiter) *gin.Engine {
	router := gin.New()
	router.POST("/login", func(c *gin.Context) {
		if userLimiter != nil {
			// simulate the username-dimension middleware
		}
		c.Status(http.StatusOK)
	})
	router.POST("/register", GinIPRateLimit(l), func(c *gin.Context) { c.Status(http.StatusCreated) })
	return router
}

func TestGinIPRateLimit_AllowsWithinLimit(t *testing.T) {
	l := New(Config{Enabled: true, Limit: 2, Duration: time.Minute})
	defer l.Close()

	router := newGinRouter(l, nil)
	req := httptest.NewRequest(http.MethodPost, "/register", nil)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusCreated, w.Code)
	}
}

func TestGinIPRateLimit_BlocksOverLimitWithRetryAfter(t *testing.T) {
	l := New(Config{Enabled: true, Limit: 1, Duration: 10 * time.Minute})
	defer l.Close()

	router := newGinRouter(l, nil)
	req := httptest.NewRequest(http.MethodPost, "/register", nil)
	req.RemoteAddr = "203.0.113.5:1234"

	// First request passes.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	// Second request blocked with 429 + Retry-After.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	assert.NotEmpty(t, w.Header().Get("Retry-After"), "429 must include Retry-After")

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	var errObj struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body["error"], &errObj))
	assert.Equal(t, "RATE_LIMITED", errObj.Code)
}

func TestGinUsernameRateLimit_BlocksByUsername(t *testing.T) {
	l := New(Config{Enabled: true, Limit: 2, Duration: time.Minute})
	defer l.Close()

	router := gin.New()
	router.POST("/login", GinUsernameRateLimit(l), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	body := `{"username":"alice","password":"x"}`
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Third attempt for same username blocked.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	// Different username still allowed.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"bob","password":"x"}`))
	req2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestGinUsernameRateLimit_RestoresBody(t *testing.T) {
	l := New(Config{Enabled: true, Limit: 5, Duration: time.Minute})
	defer l.Close()

	router := gin.New()
	router.POST("/login", GinUsernameRateLimit(l), func(c *gin.Context) {
		var req struct {
			Username string `json:"username"`
		}
		// Body must still be readable after the middleware restored it.
		if err := c.ShouldBindJSON(&req); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		if req.Username == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "handler must still bind the body after username limiter")
}
