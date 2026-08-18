package authorization

import (
	"net/http"
	"net/http/httptest"
	"testing"

	casbinrbac "vue-element-plus-admin/backend/internal/middleware/casbin"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/gin-gonic/gin"
)

const permissionTestModel = `[request_definition]
r = sub, obj
[policy_definition]
p = sub, obj
[role_definition]
g = _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj`

func permissionTestEnforcer(t *testing.T, allow bool) *casbin.SyncedEnforcer {
	t.Helper()
	accessModel, err := model.NewModelFromString(permissionTestModel)
	if err != nil {
		t.Fatal(err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(accessModel)
	if err != nil {
		t.Fatal(err)
	}
	if allow {
		role := casbinrbac.RoleSubject("tester")
		if _, err := enforcer.AddRoleForUser(casbinrbac.UserSubject(7), role); err != nil {
			t.Fatal(err)
		}
		if _, err := enforcer.AddPermissionForUser(role, UserRead); err != nil {
			t.Fatal(err)
		}
	}
	return enforcer
}

func permissionTestResponse(enforcer *casbin.SyncedEnforcer, withIdentity bool) *httptest.ResponseRecorder {
	router := gin.New()
	router.GET("/protected", func(c *gin.Context) {
		if withIdentity {
			c.Set(jwtservice.UserIDContextKey, int64(7))
		}
		c.Next()
	}, RequirePermission(enforcer, UserRead), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/protected", nil))
	return recorder
}

func TestRequirePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("allows an authorized user", func(t *testing.T) {
		recorder := permissionTestResponse(permissionTestEnforcer(t, true), true)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	})

	t.Run("rejects a user without the server-side permission", func(t *testing.T) {
		recorder := permissionTestResponse(permissionTestEnforcer(t, false), true)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("rejects a missing identity", func(t *testing.T) {
		recorder := permissionTestResponse(permissionTestEnforcer(t, true), false)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
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
