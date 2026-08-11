//go:build rollback

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/authorization"
)

type fakeAuthorizationService struct {
	allowed        bool
	err            error
	userID         int64
	permissionCode string
	calls          int
}

func (f *fakeAuthorizationService) EffectivePermissions(context.Context, int64) ([]string, error) {
	return []string{}, nil
}

func (f *fakeAuthorizationService) HasPermission(_ context.Context, userID int64, permissionCode string) (bool, error) {
	f.calls++
	f.userID = userID
	f.permissionCode = permissionCode
	return f.allowed, f.err
}

func permissionRouter(svc authorization.Service, injectPrincipal bool, reached *bool) *gin.Engine {
	router := gin.New()
	router.GET("/protected", func(c *gin.Context) {
		if injectPrincipal {
			c.Set(auth.ContextAuthUser, &auth.AuthUser{ID: 42, Username: "alice"})
		}
		c.Next()
	}, RequirePermission(svc, authorization.UsersRead), func(c *gin.Context) {
		*reached = true
		c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
	})
	return router
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope.Error.Code
}

func TestRequirePermissionAllowsAuthorizedPrincipal(t *testing.T) {
	svc := &fakeAuthorizationService{allowed: true}
	reached := false
	router := permissionRouter(svc, true, &reached)

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.True(t, reached)
	assert.Equal(t, 1, svc.calls)
	assert.Equal(t, int64(42), svc.userID)
	assert.Equal(t, authorization.UsersRead, svc.permissionCode)
}

func TestRequirePermissionRejectsMissingPrincipal(t *testing.T) {
	svc := &fakeAuthorizationService{allowed: true}
	reached := false
	router := permissionRouter(svc, false, &reached)

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Equal(t, "AUTH_INVALID_TOKEN", errorCode(t, response))
	assert.False(t, reached)
	assert.Zero(t, svc.calls)
}

func TestRequirePermissionRejectsDeniedPrincipal(t *testing.T) {
	svc := &fakeAuthorizationService{allowed: false}
	reached := false
	router := permissionRouter(svc, true, &reached)

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Equal(t, "AUTH_FORBIDDEN", errorCode(t, response))
	assert.False(t, reached)
}

func TestRequirePermissionReturnsInternalEnvelopeOnQueryFailure(t *testing.T) {
	svc := &fakeAuthorizationService{err: errors.New("database unavailable")}
	reached := false
	router := permissionRouter(svc, true, &reached)

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Equal(t, "INTERNAL_ERROR", errorCode(t, response))
	assert.False(t, reached)
}
