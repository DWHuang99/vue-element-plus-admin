package oauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	oidcCustom "vue-element-plus-admin/backend/internal/middleware/oidc"
	"vue-element-plus-admin/backend/internal/security"

	"github.com/alicebob/miniredis/v2"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

func newFlowTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	return server, redisClient
}

func newFlowTestService(t *testing.T) (*ExternalUserService, *miniredis.Miniredis) {
	t.Helper()
	server, redisClient := newFlowTestRedis(t)
	return &ExternalUserService{redisClient: redisClient}, server
}

func TestPopFlowIsOneTime(t *testing.T) {
	service, _ := newFlowTestService(t)
	want := oidcCustom.LoginFlow{
		Nonce:     "nonce",
		Verifier:  "verifier",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := service.StoreFlow("state", want, context.Background()); err != nil {
		t.Fatalf("StoreFlow() error = %v", err)
	}

	got, exists := service.PopFlow("state", context.Background())
	if !exists || got.Nonce != want.Nonce || got.Verifier != want.Verifier || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("first PopFlow() = (%+v, %v), want (%+v, true)", got, exists, want)
	}
	if _, exists := service.PopFlow("state", context.Background()); exists {
		t.Fatal("second PopFlow() found a state that should have been consumed")
	}
}

func TestFlowCanBeConsumedByAnotherInstance(t *testing.T) {
	server := miniredis.RunT(t)
	firstClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	secondClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = firstClient.Close()
		_ = secondClient.Close()
	})
	firstService := &ExternalUserService{redisClient: firstClient}
	secondService := &ExternalUserService{redisClient: secondClient}
	want := oidcCustom.LoginFlow{
		Nonce:     "cross-instance-nonce",
		Verifier:  "cross-instance-verifier",
		ExpiresAt: time.Now().Add(time.Minute),
	}

	if err := firstService.StoreFlow("cross-instance-state", want, context.Background()); err != nil {
		t.Fatalf("StoreFlow() error = %v", err)
	}
	got, exists := secondService.PopFlow("cross-instance-state", context.Background())
	if !exists || got.Nonce != want.Nonce || got.Verifier != want.Verifier || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("PopFlow() = (%+v, %v), want (%+v, true)", got, exists, want)
	}
}

func TestFlowExpiresInRedis(t *testing.T) {
	service, server := newFlowTestService(t)
	flow := oidcCustom.LoginFlow{
		Nonce:     "nonce",
		Verifier:  "verifier",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	if err := service.StoreFlow("expiring-state", flow, context.Background()); err != nil {
		t.Fatalf("StoreFlow() error = %v", err)
	}
	server.FastForward(5*time.Minute + time.Second)
	if _, exists := service.PopFlow("expiring-state", context.Background()); exists {
		t.Fatal("PopFlow() found an expired state")
	}
}

func TestExternalAccountPasswordHash(t *testing.T) {
	passwordHash, err := externalAccountPasswordHash()
	if err != nil {
		t.Fatalf("externalAccountPasswordHash() error = %v", err)
	}
	if _, err := bcrypt.Cost([]byte(passwordHash)); err != nil {
		t.Fatalf("external account password hash is not bcrypt: %v", err)
	}
	if security.Verify("", passwordHash) {
		t.Fatal("external account can authenticate with an empty password")
	}
}

func TestExternalUsernameIsValidAndIdentityScoped(t *testing.T) {
	tests := []struct {
		name        string
		displayName string
	}{
		{name: "empty display name", displayName: ""},
		{name: "symbols only", displayName: " !@#$ "},
		{name: "long unicode name", displayName: strings.Repeat("用户", 40)},
		{name: "normal name", displayName: "Alice Example"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			username := externalUsername(test.displayName, "https://issuer.example", "subject-1")
			length := utf8.RuneCountInString(username)
			if length < 3 || length > 50 {
				t.Fatalf("externalUsername() rune length = %d, want 3..50: %q", length, username)
			}
			if username != externalUsername(test.displayName, "https://issuer.example", "subject-1") {
				t.Fatal("externalUsername() is not deterministic")
			}
			otherIdentityUsername := externalUsername(test.displayName, "https://issuer.example", "subject-2")
			if username == otherIdentityUsername {
				t.Fatalf("different subjects generated the same username %q", username)
			}
		})
	}
}

func TestFindOrCreateUserRejectsInvalidIdentity(t *testing.T) {
	service := &ExternalUserService{}
	_, err := service.FindOrCreateUser(context.Background(), &oidc.IDToken{}, IDTokenClaims{})
	if !errors.Is(err, ErrInvalidExternalIdentity) {
		t.Fatalf("FindOrCreateUser() error = %v, want %v", err, ErrInvalidExternalIdentity)
	}
}
