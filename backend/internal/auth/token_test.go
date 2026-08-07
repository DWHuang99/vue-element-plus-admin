package auth

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateToken_Unique(t *testing.T) {
	t1, err := GenerateToken()
	require.NoError(t, err)
	t2, err := GenerateToken()
	require.NoError(t, err)

	assert.NotEqual(t, t1, t2, "tokens must be unique")
}

func TestGenerateToken_Length(t *testing.T) {
	token, err := GenerateToken()
	require.NoError(t, err)
	// base64url of 32 bytes without padding = 43 chars
	assert.Equal(t, 43, len(token))
}

func TestHashToken_Is64CharHex(t *testing.T) {
	token, err := GenerateToken()
	require.NoError(t, err)

	hash := HashToken(token)
	assert.Equal(t, 64, len(hash), "SHA-256 hex digest is 64 chars")

	_, err = hex.DecodeString(hash)
	assert.NoError(t, err, "hash should be valid hex")
}

func TestHashToken_Deterministic(t *testing.T) {
	token := "abc123"
	h1 := HashToken(token)
	h2 := HashToken(token)
	assert.Equal(t, h1, h2, "hash of same token must be identical")
}

func TestHashToken_DiffersFromRaw(t *testing.T) {
	token, err := GenerateToken()
	require.NoError(t, err)
	hash := HashToken(token)

	assert.NotEqual(t, token, hash, "hash must never equal the raw token")
}

func TestSessionExpiry(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, now.Add(24*time.Hour), SessionExpiry(now))
}

func TestSessionIsValid_FreshSession(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour)
	expires := SessionExpiry(created)

	assert.True(t, SessionIsValid(now, created, expires))
}

func TestSessionIsValid_IdleExpired(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	created := now.Add(-24 * time.Hour)
	expires := now.Add(-time.Second) // idle > 24h → expired

	assert.False(t, SessionIsValid(now, created, expires))
}

func TestSessionIsValid_ExceedsAbsoluteCap(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	created := now.Add(-8 * 24 * time.Hour) // 8 days old > 7-day cap
	expires := now.Add(time.Hour)           // idle OK, but absolute cap exceeded

	assert.False(t, SessionIsValid(now, created, expires))
}

func TestSessionIsValid_BoundaryAtCap(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	created := now.Add(-7 * 24 * time.Hour) // exactly at 7-day cap
	expires := now.Add(time.Hour)

	assert.False(t, SessionIsValid(now, created, expires), "at cap boundary session is invalid (>=)")

	createdJustBefore := created.Add(time.Minute)
	assert.True(t, SessionIsValid(now, createdJustBefore, expires), "just under cap is valid")
}

func TestRenewExpiry(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, now.Add(24*time.Hour), RenewExpiry(now))
}
