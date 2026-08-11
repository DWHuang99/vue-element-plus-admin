// readChain unit tests (T075): the shadow stage must sit BEFORE the
// authorization check as its own chain member — RequirePermission calls
// c.Next() internally, so a shadow wrapped after it would replace the writer
// after the handler already wrote (empty capture). Shadow-first also keeps
// the comparison running when authz rejects: the handler is aborted, but the
// 403 it wrote is exactly what the legacy replay must be compared against.
package http

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestReadChain_NilShadow(t *testing.T) {
	authz := func(c *gin.Context) { c.String(http.StatusOK, "authz-only") }
	chain := readChain(authz, nil)
	require.Len(t, chain, 1)
	require.True(t, reflect.ValueOf(chain[0]).Pointer() == reflect.ValueOf(authz).Pointer(),
		"nil shadow must drop out leaving the authz function alone")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	chain[0](c)
	require.Equal(t, "authz-only", rec.Body.String())
}

func TestReadChain_ShadowCapturesHandlerWrite(t *testing.T) {
	// Regression for the empty-capture bug: an authz that calls c.Next()
	// (like RequirePermission) would let the handler write BEFORE a shadow
	// wrapped after it ran. Shadow-first must place the handler's write
	// inside the shadow's own c.Next() span.
	var order []string
	authz := func(c *gin.Context) {
		order = append(order, "authz")
		c.Next()
		order = append(order, "authz-after-next")
	}
	shadow := func(c *gin.Context) {
		order = append(order, "shadow-before")
		c.Next()
		order = append(order, "shadow-after")
	}
	handler := func(c *gin.Context) { order = append(order, "handler") }

	router := gin.New()
	router.GET("/api/v1/roles", append(readChain(authz, shadow), handler)...)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil))

	require.Equal(t,
		[]string{"shadow-before", "authz", "handler", "authz-after-next", "shadow-after"},
		order,
		"the handler write must happen inside the shadow's capture span")
}

func TestReadChain_ShadowComparisonRunsOnAuthzRejection(t *testing.T) {
	var after []string
	shadow := func(c *gin.Context) {
		c.Next()
		after = append(after, "shadow-compare")
	}
	authz := func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "denied"})
	}
	handler := func(c *gin.Context) { t.Error("handler must not run after authz rejection") }

	router := gin.New()
	router.GET("/api/v1/roles", append(readChain(authz, shadow), handler)...)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil))

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, []string{"shadow-compare"}, after,
		"the shadow comparison must still run when authz rejects — a 403 vs 200 divergence is the signal")
}
