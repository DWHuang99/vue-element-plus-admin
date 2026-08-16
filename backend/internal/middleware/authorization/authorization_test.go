package authorization

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

type permissionCheckerStub struct {
	allowed        bool
	err            error
	userID         int64
	permissionCode string
}

func (s *permissionCheckerStub) HasPermission(_ context.Context, userID int64, permissionCode string) (bool, error) {
	s.userID = userID
	s.permissionCode = permissionCode
	return s.allowed, s.err
}

func permissionTestResponse(checker PermissionChecker, withIdentity bool) *httptest.ResponseRecorder {
	router := gin.New()
	router.GET("/protected", func(c *gin.Context) {
		if withIdentity {
			c.Set(jwtservice.UserIDContextKey, int64(7))
		}
		c.Next()
	}, RequirePermission(checker, UserRead), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/protected", nil))
	return recorder
}

func TestRequirePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("allows an authorized user", func(t *testing.T) {
		checker := &permissionCheckerStub{allowed: true}
		recorder := permissionTestResponse(checker, true)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
		if checker.userID != 7 || checker.permissionCode != UserRead {
			t.Fatalf("permission check = (%d, %q)", checker.userID, checker.permissionCode)
		}
	})

	t.Run("rejects a user without the server-side permission", func(t *testing.T) {
		recorder := permissionTestResponse(&permissionCheckerStub{}, true)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("rejects a missing identity", func(t *testing.T) {
		recorder := permissionTestResponse(&permissionCheckerStub{allowed: true}, false)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
		}
	})

	t.Run("does not expose checker failures", func(t *testing.T) {
		recorder := permissionTestResponse(&permissionCheckerStub{err: errors.New("database unavailable")}, true)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
	})
}

func TestRequireTokenPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		permissions []string
		wantStatus  int
	}{
		{name: "exact permission", permissions: []string{DepartmentRead}, wantStatus: http.StatusNoContent},
		{name: "administrator wildcard", permissions: []string{"*.*.*"}, wantStatus: http.StatusNoContent},
		{name: "missing permission", permissions: []string{UserRead}, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/protected", func(c *gin.Context) {
				c.Set(jwtservice.PermissionsContextKey, test.permissions)
			}, RequireTokenPermission(DepartmentRead), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/protected", nil))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}
