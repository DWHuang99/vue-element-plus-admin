package middleware

import (
	"net/http"
	"net/http/httptest"
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
