//go:build rollback

package rbac

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/authorization"
)

type routeAuthorization struct {
	allowedPermission string
	seenPermission    string
}

func (r *routeAuthorization) EffectivePermissions(context.Context, int64) ([]string, error) {
	return []string{}, nil
}

func (r *routeAuthorization) HasPermission(_ context.Context, _ int64, permissionCode string) (bool, error) {
	r.seenPermission = permissionCode
	return permissionCode == r.allowedPermission, nil
}

func TestRegisterRoutesBindsEndpointPermissions(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		permission string
	}{
		{name: "list roles", method: http.MethodGet, path: "/api/v1/roles", permission: authorization.RolesRead},
		{name: "save role", method: http.MethodPost, path: "/api/v1/roles", body: `{"name":"运营","code":"operator"}`, permission: authorization.RolesWrite},
		{name: "delete roles", method: http.MethodPost, path: "/api/v1/roles/delete", body: `{"ids":[9]}`, permission: authorization.RolesWrite},
		{name: "list departments", method: http.MethodGet, path: "/api/v1/departments", permission: authorization.DepartmentsRead},
		{name: "save department", method: http.MethodPost, path: "/api/v1/departments", body: `{"name":"测试部"}`, permission: authorization.DepartmentsWrite},
		{name: "delete departments", method: http.MethodPost, path: "/api/v1/departments/delete", body: `{"ids":[9]}`, permission: authorization.DepartmentsWrite},
		{name: "list users", method: http.MethodGet, path: "/api/v1/users", permission: authorization.UsersRead},
		{name: "save user", method: http.MethodPost, path: "/api/v1/users", body: `{"username":"test_user","password":"password-123"}`, permission: authorization.UsersWrite},
		{name: "delete users", method: http.MethodPost, path: "/api/v1/users/delete", body: `{"ids":[9]}`, permission: authorization.UsersWrite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorizer := &routeAuthorization{allowedPermission: tt.permission}
			handler := NewHandler(&mockService{}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(auth.ContextAuthUser, &auth.AuthUser{ID: 1, Username: "admin"})
				c.Next()
			})
			RegisterRoutes(router.Group("/api/v1"), handler, authorizer)

			request := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, tt.permission, authorizer.seenPermission)
		})
	}
}

func TestRegisterRoutesDeniesMissingEndpointPermission(t *testing.T) {
	authorizer := &routeAuthorization{allowedPermission: authorization.UsersRead}
	handler := NewHandler(&mockService{}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(auth.ContextAuthUser, &auth.AuthUser{ID: 1, Username: "reader"})
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), handler, authorizer)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/users/delete", bytes.NewBufferString(`{"ids":[9]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "AUTH_FORBIDDEN")
	assert.Equal(t, authorization.UsersWrite, authorizer.seenPermission)
}
