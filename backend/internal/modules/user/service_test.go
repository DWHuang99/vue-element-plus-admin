package user

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type repositoryStub struct {
	user *CurrentUser
	err  error
}

func (s repositoryStub) GetUserByUsername(context.Context, string) (*CurrentUser, error) {
	return s.user, s.err
}

func TestGetUserByUsername(t *testing.T) {
	t.Run("returns active user", func(t *testing.T) {
		want := &CurrentUser{Username: "admin", IsActive: true}
		service := NewService(repositoryStub{user: want})

		got, err := service.GetUserByUsername(context.Background(), "admin")

		if err != nil {
			t.Fatalf("GetUserByUsername() error = %v", err)
		}
		if got != want {
			t.Fatalf("GetUserByUsername() = %#v, want %#v", got, want)
		}
	})

	t.Run("maps missing database row", func(t *testing.T) {
		service := NewService(repositoryStub{err: pgx.ErrNoRows})

		_, err := service.GetUserByUsername(context.Background(), "missing")

		if !errors.Is(err, ErrUserNotExists) {
			t.Fatalf("GetUserByUsername() error = %v, want %v", err, ErrUserNotExists)
		}
	})

	t.Run("rejects disabled user", func(t *testing.T) {
		service := NewService(repositoryStub{user: &CurrentUser{Username: "disabled"}})

		_, err := service.GetUserByUsername(context.Background(), "disabled")

		if !errors.Is(err, ErrUserDisabled) {
			t.Fatalf("GetUserByUsername() error = %v, want %v", err, ErrUserDisabled)
		}
	})
}
