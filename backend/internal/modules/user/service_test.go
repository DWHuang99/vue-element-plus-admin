package user

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	casbinrbac "vue-element-plus-admin/backend/internal/middleware/casbin"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
)

const userServiceTestModel = `[request_definition]
r = sub, obj
[policy_definition]
p = sub, obj
[role_definition]
g = _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj`

func userServiceTestEnforcer(t *testing.T) *casbin.SyncedEnforcer {
	t.Helper()
	accessModel, err := model.NewModelFromString(userServiceTestModel)
	if err != nil {
		t.Fatal(err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(accessModel)
	if err != nil {
		t.Fatal(err)
	}
	role := casbinrbac.RoleSubject("admin")
	if _, err := enforcer.AddRoleForUser(casbinrbac.UserSubject(1), role); err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.AddPermissionForUser(role, "*.*.*"); err != nil {
		t.Fatal(err)
	}
	return enforcer
}

type repositoryStub struct {
	user *CurrentUser
	err  error
}

func (s repositoryStub) GetUserByID(context.Context, int64) (*CurrentUser, error) {
	return s.user, s.err
}

func TestGetUserByID(t *testing.T) {
	t.Run("returns active user", func(t *testing.T) {
		want := &CurrentUser{ID: 1, Username: "admin", IsActive: true}
		service := NewService(repositoryStub{user: want}, userServiceTestEnforcer(t))

		got, err := service.GetUserByID(context.Background(), 1)

		if err != nil {
			t.Fatalf("GetUserByID() error = %v", err)
		}
		if got != want {
			t.Fatalf("GetUserByID() = %#v, want %#v", got, want)
		}
	})

	t.Run("maps missing database row", func(t *testing.T) {
		service := NewService(repositoryStub{err: sql.ErrNoRows}, nil)

		_, err := service.GetUserByID(context.Background(), 999)

		if !errors.Is(err, ErrUserNotExists) {
			t.Fatalf("GetUserByID() error = %v, want %v", err, ErrUserNotExists)
		}
	})

	t.Run("rejects disabled user", func(t *testing.T) {
		service := NewService(repositoryStub{user: &CurrentUser{Username: "disabled"}}, nil)

		_, err := service.GetUserByID(context.Background(), 2)

		if !errors.Is(err, ErrUserDisabled) {
			t.Fatalf("GetUserByID() error = %v, want %v", err, ErrUserDisabled)
		}
	})
}
