//go:build rollback

package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	require.NoError(t, err)

	ok, err := VerifyPassword(hash, "supersecret123")
	require.NoError(t, err)
	assert.True(t, ok, "correct password should verify")
}

func TestHashPassword_WrongPassword(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	require.NoError(t, err)

	ok, err := VerifyPassword(hash, "wrongpassword")
	require.NoError(t, err)
	assert.False(t, ok, "wrong password should not verify")
}

func TestHashPassword_UniqueSalt(t *testing.T) {
	h1, err := HashPassword("same-password")
	require.NoError(t, err)
	h2, err := HashPassword("same-password")
	require.NoError(t, err)

	assert.NotEqual(t, h1, h2, "same password with different salts must produce different hashes")
}

func TestHashPassword_PHCPrefix(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$"), "hash should be PHC-formatted argon2id")
}

func TestHashPassword_MalformedHashRejected(t *testing.T) {
	_, err := VerifyPassword("not-a-valid-hash", "password")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidHash)
}

func TestVerifyPassword_MalformedEncoding(t *testing.T) {
	// Correct prefix but bad base64
	_, err := VerifyPassword("$argon2id$v=19$m=65536,t=3,p=4$!!not-base64!!$!!bad!!", "password")
	require.Error(t, err)
}

func TestHashPassword_NotPlaintext(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	require.NoError(t, err)
	assert.NotContains(t, hash, "supersecret123", "hash must not contain the plaintext password")
}

func TestVerifyPassword_EmptyPassword(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	require.NoError(t, err)
	ok, err := VerifyPassword(hash, "")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestDecodeHash_RejectsUnsupportedVersion(t *testing.T) {
	_, _, _, err := decodeHash("$argon2id$v=999$m=65536,t=3,p=4$c2FsdHNhbHQ$c2FsdHNhbHQ")
	assert.Error(t, err)
}
