package jwtservice

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestJWKSRemoteVerificationFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	router := gin.New()
	RegisterJWKSRoutes(router, NewJWKSHandler(&privateKey.PublicKey, testKeyID))
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + JWKSPath)
	if err != nil {
		t.Fatalf("get JWKS: %v", err)
	}
	defer response.Body.Close()
	var document JWKS
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode JWKS: %v", err)
	}
	if len(document.Keys) != 1 || document.Keys[0].KeyID != testKeyID {
		t.Fatalf("JWKS = %#v, want key ID %q", document, testKeyID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remoteKeyfunc, err := NewRemoteJWKSKeyfunc(ctx, server.URL+JWKSPath)
	if err != nil {
		t.Fatalf("NewRemoteJWKSKeyfunc() error = %v", err)
	}
	issuer := NewJWTManager(
		privateKey,
		&privateKey.PublicKey,
		testKeyID,
		testIssuer,
		testAudience,
		time.Minute,
		time.Hour,
	)
	verifier := NewJWTVerifier(remoteKeyfunc, testIssuer, testAudience)

	token, err := issuer.GenerateToken(42, []string{"user"})
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	claims, err := verifier.ParseToken(token)
	if err != nil {
		t.Fatalf("remote ParseToken() error = %v", err)
	}
	if claims.Subject != "42" {
		t.Fatalf("Subject = %q, want 42", claims.Subject)
	}
}
