package jwtservice

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testKeyID    = "test-key"
	testIssuer   = "test-issuer"
	testAudience = "test-api"
)

func newTestJWTManager(t *testing.T) *JWTManager {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return NewJWTManager(
		privateKey,
		&privateKey.PublicKey,
		testKeyID,
		testIssuer,
		testAudience,
		time.Minute,
		7*24*time.Hour,
	)
}

func TestGenerateAndParseToken(t *testing.T) {
	manager := newTestJWTManager(t)

	token, err := manager.GenerateTokenWithPermissions(42, []string{"user"}, []string{"system:department:read"})
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	claims, err := manager.ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken() error = %v", err)
	}
	if claims.Subject != "42" {
		t.Fatalf("Subject = %q, want %q", claims.Subject, "42")
	}
	if len(claims.Role) != 1 || claims.Role[0] != "user" {
		t.Fatalf("Role = %v, want [user]", claims.Role)
	}
	if len(claims.Permissions) != 1 || claims.Permissions[0] != "system:department:read" {
		t.Fatalf("Permissions = %v", claims.Permissions)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != testAudience {
		t.Fatalf("Audience = %v, want [%s]", claims.Audience, testAudience)
	}

	parsed, _, err := new(jwt.Parser).ParseUnverified(token, &Claims{})
	if err != nil {
		t.Fatalf("ParseUnverified() error = %v", err)
	}
	if parsed.Method.Alg() != jwt.SigningMethodRS256.Alg() {
		t.Fatalf("algorithm = %q, want RS256", parsed.Method.Alg())
	}
	if parsed.Header["kid"] != testKeyID {
		t.Fatalf("kid = %#v, want %q", parsed.Header["kid"], testKeyID)
	}
}

func TestParseTokenRejectsMissingKIDAndWrongAudience(t *testing.T) {
	manager := newTestJWTManager(t)
	now := time.Now()

	tests := []struct {
		name     string
		audience string
		setKID   bool
	}{
		{name: "missing kid", audience: testAudience},
		{name: "wrong audience", audience: "other-api", setKID: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := Claims{
				Role: []string{"user"},
				RegisteredClaims: jwt.RegisteredClaims{
					Issuer:    testIssuer,
					Subject:   "42",
					Audience:  jwt.ClaimStrings{test.audience},
					IssuedAt:  jwt.NewNumericDate(now),
					ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
				},
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			if test.setKID {
				token.Header["kid"] = testKeyID
			}
			signed, err := token.SignedString(manager.privateKey)
			if err != nil {
				t.Fatalf("sign token: %v", err)
			}
			if _, err := manager.ParseToken(signed); err == nil {
				t.Fatal("ParseToken() error = nil, want invalid token")
			}
		})
	}
}
