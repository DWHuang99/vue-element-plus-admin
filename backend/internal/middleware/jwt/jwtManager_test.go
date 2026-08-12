package jwtservice

import (
	"testing"
	"time"
)

func TestGenerateAndParseToken(t *testing.T) {
	manager := NewJWTManager("test-secret", "test-issuer", time.Minute, 7*24*time.Hour)

	token, err := manager.GenerateToken("test-user", []string{"user"})
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	claims, err := manager.ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken() error = %v", err)
	}
	if claims.Subject != "test-user" {
		t.Fatalf("Subject = %q, want %q", claims.Subject, "test-user")
	}
	if len(claims.Role) != 1 || claims.Role[0] != "user" {
		t.Fatalf("Role = %v, want [user]", claims.Role)
	}
}
