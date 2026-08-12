package rdb

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewRefreshTokenUses256Bits(t *testing.T) {
	token, err := newRefreshToken()
	if err != nil {
		t.Fatalf("newRefreshToken() error = %v", err)
	}

	randomBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("refresh token is not base64url: %v", err)
	}
	if len(randomBytes) != 32 {
		t.Fatalf("refresh token contains %d bytes, want 32", len(randomBytes))
	}
}

func TestRefreshTokenKeyDoesNotExposeToken(t *testing.T) {
	token := "raw-refresh-token"
	key := refreshTokenKey(token)

	if !strings.HasPrefix(key, refreshTokenPrefix) {
		t.Fatalf("key = %q, want prefix %q", key, refreshTokenPrefix)
	}
	if strings.Contains(key, token) {
		t.Fatal("Redis key exposes the raw refresh token")
	}
}
