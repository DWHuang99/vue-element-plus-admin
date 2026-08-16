package user

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type repositoryStub struct {
	user *CurrentUser
	err  error
}

func (s repositoryStub) GetUserByID(context.Context, int64) (*CurrentUser, error) {
	return s.user, s.err
}

func TestGetUserByID(t *testing.T) {
	t.Run("returns active user", func(t *testing.T) {
		want := &CurrentUser{Username: "admin", IsActive: true}
		service := NewService(repositoryStub{user: want})

		got, err := service.GetUserByID(context.Background(), 1)

		if err != nil {
			t.Fatalf("GetUserByID() error = %v", err)
		}
		if got != want {
			t.Fatalf("GetUserByID() = %#v, want %#v", got, want)
		}
	})

	t.Run("maps missing database row", func(t *testing.T) {
		service := NewService(repositoryStub{err: sql.ErrNoRows})

		_, err := service.GetUserByID(context.Background(), 999)

		if !errors.Is(err, ErrUserNotExists) {
			t.Fatalf("GetUserByID() error = %v, want %v", err, ErrUserNotExists)
		}
	})

	t.Run("rejects disabled user", func(t *testing.T) {
		service := NewService(repositoryStub{user: &CurrentUser{Username: "disabled"}})

		_, err := service.GetUserByID(context.Background(), 2)

		if !errors.Is(err, ErrUserDisabled) {
			t.Fatalf("GetUserByID() error = %v, want %v", err, ErrUserDisabled)
		}
	})
}
