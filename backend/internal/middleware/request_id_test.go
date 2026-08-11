package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		id, exists := c.Get(RequestIDHeader)
		assert.True(t, exists)
		assert.NotEmpty(t, id)
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify header is set in response
	respID := w.Header().Get(RequestIDHeader)
	assert.NotEmpty(t, respID)

	// Verify it looks like a UUID (36 chars with dashes)
	assert.Len(t, respID, 36)
	assert.Contains(t, respID, "-")
}

func TestRequestID_PreservesWhenPresent(t *testing.T) {
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		id, exists := c.Get(RequestIDHeader)
		assert.True(t, exists)
		assert.Equal(t, "my-custom-id-123", id)
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(RequestIDHeader, "my-custom-id-123")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "my-custom-id-123", w.Header().Get(RequestIDHeader))
}

func TestRequestID_UUIDFormat(t *testing.T) {
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Generate 10 IDs and verify they are all different and valid UUIDs
	ids := make(map[string]bool)
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		id := w.Header().Get(RequestIDHeader)
		assert.Len(t, id, 36)
		assert.False(t, ids[id], "IDs should be unique")
		ids[id] = true
	}
}

func TestRequestID_PresentInContext(t *testing.T) {
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		id, exists := c.Get(RequestIDHeader)
		require.True(t, exists)
		require.NotNil(t, id)
		c.String(http.StatusOK, id.(string))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Body.String())
}

func TestRequestID_HeaderConstants(t *testing.T) {
	assert.Equal(t, "X-Request-Id", RequestIDHeader)
	assert.Equal(t, "X-Response-Time", ResponseTimeHeader)
}

// T073: the request ID is bounded correlation context — client-supplied
// values outside the restricted charset are discarded, never echoed.
func TestRequestID_RejectsInvalidCharset(t *testing.T) {
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	invalid := []string{
		"has space",
		"tab\tid",
		"unicode-汉字",
		"quote\"inject",
		"newline\ninject",
		".leading-dot",           // first char must be alphanumeric
		"$dollar",                // $ outside charset
		strings.Repeat("a", 129), // overlong
	}
	for _, in := range invalid {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set(RequestIDHeader, in)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		got := w.Header().Get(RequestIDHeader)
		assert.NotEqual(t, in, got, "invalid id %q must not be echoed", in)
		assert.True(t, ValidRequestID(got), "regenerated id must fit the charset: %q", got)
		assert.Len(t, got, 36, "regenerated id should be a UUID: %q", got)
	}
}

// T073: values inside the restricted charset pass through verbatim.
func TestRequestID_PreservesValidCharsetVariants(t *testing.T) {
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	valid := []string{
		"my-custom-id-123",
		"a.b:c_d-e",              // every extra charset member
		"12345",                  // digits only
		"Z",                      // single char
		strings.Repeat("a", 128), // exact max length
	}
	for _, in := range valid {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set(RequestIDHeader, in)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, in, w.Header().Get(RequestIDHeader), "valid id %q must be preserved", in)
	}
}

// T073: the request ID is never derived from the token — a request carrying
// credentials but no request ID gets a fresh opaque UUID, and a client
// request ID is never rewritten from credential material.
func TestRequestID_NeverDerivedFromToken(t *testing.T) {
	router := gin.New()
	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer tok-abc123")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	got := w.Header().Get(RequestIDHeader)
	assert.NotEqual(t, "tok-abc123", got)
	assert.NotContains(t, got, "tok-abc123", "request ID must not be derived from the token")
	assert.Len(t, got, 36)
}
