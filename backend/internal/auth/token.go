package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// Session lifecycle constants.
//
// Lifecycle rule (documented in data-model.md): a session is valid while
//   - revoked_at IS NULL, AND
//   - now < expires_at      (idle timeout: renewed to now+SessionTTL on each use), AND
//   - now < created_at + MaxSessionLifetime   (absolute cap: no session outlives 7 days)
const (
	// TokenBytes is the number of random bytes in a session token (32B → 256 bits).
	TokenBytes = 32
	// SessionTTL is the idle lifetime of a session between uses (sliding window).
	SessionTTL = 24 * time.Hour
	// MaxSessionLifetime is the hard ceiling on total session lifetime from creation.
	MaxSessionLifetime = 7 * 24 * time.Hour
)

// GenerateToken creates a new cryptographically random opaque session token.
// Returns a base64url-encoded string.
func GenerateToken() (string, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken returns the SHA-256 hex digest of a raw token.
// Only the hash is ever stored in the database.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionExpiry computes expires_at for a newly created session: now + SessionTTL.
func SessionExpiry(now time.Time) time.Time {
	return now.Add(SessionTTL)
}

// SessionIsValid reports whether a session is usable at time now.
//   - expiresAt: absolute idle-expiry (slid forward on each authenticated use)
//   - createdAt: session creation time (absolute cap anchor)
func SessionIsValid(now, createdAt, expiresAt time.Time) bool {
	if now.After(expiresAt) || now.Equal(expiresAt) {
		return false // idle > SessionTTL
	}
	if now.After(createdAt.Add(MaxSessionLifetime)) || now.Equal(createdAt.Add(MaxSessionLifetime)) {
		return false // exceeds absolute 7-day lifetime cap
	}
	return true
}

// RenewExpiry returns the new expires_at when an authenticated session is slid forward.
func RenewExpiry(now time.Time) time.Time {
	return now.Add(SessionTTL)
}
