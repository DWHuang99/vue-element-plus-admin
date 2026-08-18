package casbinrbac

import (
	"testing"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
)

func newTestEnforcer(t *testing.T) *casbin.SyncedEnforcer {
	t.Helper()
	accessModel, err := model.NewModelFromString(modelText)
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(accessModel)
	if err != nil {
		t.Fatalf("create enforcer: %v", err)
	}
	return enforcer
}

func TestEnforcerSupportsMultipleRolesAndWildcard(t *testing.T) {
	enforcer := newTestEnforcer(t)
	operator := RoleSubject("operator")
	auditor := RoleSubject("auditor")
	if _, err := enforcer.AddPermissionForUser(operator, "system:user:read"); err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.AddPermissionForUser(auditor, "system:audit:read"); err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.AddRolesForUser(UserSubject(7), []string{operator, auditor}); err != nil {
		t.Fatal(err)
	}

	for _, permissionCode := range []string{"system:user:read", "system:audit:read"} {
		allowed, err := enforcer.Enforce(UserSubject(7), permissionCode)
		if err != nil || !allowed {
			t.Fatalf("permission %q: allowed=%v err=%v", permissionCode, allowed, err)
		}
	}
	roles, err := enforcer.GetImplicitRolesForUser(UserSubject(7))
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := enforcer.GetImplicitPermissionsForUser(UserSubject(7))
	if err != nil {
		t.Fatal(err)
	}
	if len(RoleCodes(roles)) != 2 || len(PermissionCodes(permissions)) != 2 {
		t.Fatalf("roles=%v permissions=%v", roles, permissions)
	}

	admin := RoleSubject("admin")
	if _, err := enforcer.AddPermissionForUser(admin, "*.*.*"); err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.AddRoleForUser(UserSubject(8), admin); err != nil {
		t.Fatal(err)
	}
	allowed, err := enforcer.Enforce(UserSubject(8), "system:anything:delete")
	if err != nil || !allowed {
		t.Fatalf("administrator wildcard: allowed=%v err=%v", allowed, err)
	}
}
