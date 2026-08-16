package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
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
