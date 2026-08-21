package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadJwtConfigLoadsMatchingRSAKeys(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	directory := t.TempDir()
	privateKeyPath := filepath.Join(directory, "private.pem")
	publicKeyPath := filepath.Join(directory, "public.pem")

	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	if err := os.WriteFile(publicKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	t.Setenv("JWT_PRIVATE_KEY_PATH", privateKeyPath)
	t.Setenv("JWT_PUBLIC_KEY_PATH", publicKeyPath)
	t.Setenv("JWT_KEY_ID", "rotation-key")
	t.Setenv("JWT_ISSUER", "https://auth.example.test")
	t.Setenv("JWT_AUDIENCE", "example-api")

	configuration, err := LoadJwtConfig()
	if err != nil {
		t.Fatalf("LoadJwtConfig() error = %v", err)
	}
	if configuration.KeyID != "rotation-key" {
		t.Fatalf("KeyID = %q, want rotation-key", configuration.KeyID)
	}
	if configuration.Issuer != "https://auth.example.test" {
		t.Fatalf("Issuer = %q, want configured issuer", configuration.Issuer)
	}
	if configuration.Audience != "example-api" {
		t.Fatalf("Audience = %q, want example-api", configuration.Audience)
	}
	if configuration.PrivateKey.PublicKey.N.Cmp(configuration.PublicKey.N) != 0 {
		t.Fatal("loaded private and public keys do not match")
	}
}

func TestLoadJwtConfigRejectsMismatchedKeys(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private RSA key: %v", err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate public RSA key: %v", err)
	}
	directory := t.TempDir()
	privateKeyPath := filepath.Join(directory, "private.pem")
	publicKeyPath := filepath.Join(directory, "public.pem")
	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&otherKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	if err := os.WriteFile(publicKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	t.Setenv("JWT_PRIVATE_KEY_PATH", privateKeyPath)
	t.Setenv("JWT_PUBLIC_KEY_PATH", publicKeyPath)

	if _, err := LoadJwtConfig(); err == nil {
		t.Fatal("LoadJwtConfig() error = nil, want mismatched-key error")
	}
}

func setValidOIDCEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER", "https://issuer.example.test/tenant")
	t.Setenv("OIDC_CLIENT_ID", "client-id")
	t.Setenv("OIDC_CLIENT_SECRET", "client-secret")
	t.Setenv("OIDC_REDIRECT_URL", "http://localhost:8080/api/v1/oauth/callback")
	t.Setenv("OIDC_FRONTEND_REDIRECT_URL", "http://localhost:4000/login")
}

func TestLoadOidcConfigAllowsDisabledProvider(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "false")
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	t.Setenv("OIDC_REDIRECT_URL", "")
	t.Setenv("OIDC_FRONTEND_REDIRECT_URL", "")

	configuration, err := LoadOidcConfig()
	if err != nil {
		t.Fatalf("LoadOidcConfig() error = %v", err)
	}
	if configuration.Enabled {
		t.Fatal("Enabled = true, want false")
	}
}

func TestLoadOidcConfig(t *testing.T) {
	setValidOIDCEnvironment(t)

	configuration, err := LoadOidcConfig()
	if err != nil {
		t.Fatalf("LoadOidcConfig() error = %v", err)
	}
	if configuration.ClientID != "client-id" {
		t.Fatalf("ClientID = %q, want client-id", configuration.ClientID)
	}
	if configuration.FrontendRedirectURL != "http://localhost:4000/login" {
		t.Fatalf("FrontendRedirectURL = %q", configuration.FrontendRedirectURL)
	}
}

func TestLoadOidcConfigRejectsMissingValue(t *testing.T) {
	setValidOIDCEnvironment(t)
	t.Setenv("OIDC_CLIENT_ID", "")

	if _, err := LoadOidcConfig(); err == nil || !strings.Contains(err.Error(), "OIDC_CLIENT_ID") {
		t.Fatalf("LoadOidcConfig() error = %v, want OIDC_CLIENT_ID error", err)
	}
}

func TestLoadOidcConfigRejectsInvalidURL(t *testing.T) {
	setValidOIDCEnvironment(t)
	t.Setenv("OIDC_REDIRECT_URL", "/api/v1/oauth/callback")

	if _, err := LoadOidcConfig(); err == nil || !strings.Contains(err.Error(), "OIDC_REDIRECT_URL") {
		t.Fatalf("LoadOidcConfig() error = %v, want OIDC_REDIRECT_URL error", err)
	}
}
