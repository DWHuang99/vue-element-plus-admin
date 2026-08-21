package auth

import (
	"context"
	"errors"
	"testing"
)

func TestLoginOIDCRejectsInvalidUserID(t *testing.T) {
	service := &AuthService{}
	_, _, err := service.LoginOIDC(context.Background(), 0)
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("LoginOIDC() error = %v, want %v", err, ErrUserNotFound)
	}
}
