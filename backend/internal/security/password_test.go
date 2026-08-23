package security

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	passwordHash, err := Hash("Test123456!")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}

	if !Verify("Test123456!", passwordHash) {
		t.Fatal("Verify() rejected the correct password")
	}
	if Verify("wrong-password", passwordHash) {
		t.Fatal("Verify() accepted an incorrect password")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte("Google OAuth access token")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(string(ciphertext)); err != nil {
		t.Fatalf("Encrypt() returned non-Base64 data: %v", err)
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Decrypt() = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptUsesUniqueNonce(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte("same token")

	first, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("first Encrypt() error = %v", err)
	}
	second, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("second Encrypt() error = %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("Encrypt() reused a nonce: identical plaintext produced identical ciphertext")
	}
}

func TestDecryptRejectsMalformedCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	tests := [][]byte{
		[]byte("not-base64"),
		[]byte(base64.StdEncoding.EncodeToString([]byte("too short"))),
	}

	for _, ciphertext := range tests {
		if _, err := Decrypt(key, ciphertext); err == nil {
			t.Fatalf("Decrypt(%q) unexpectedly succeeded", ciphertext)
		}
	}
}
