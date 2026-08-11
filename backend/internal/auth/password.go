//go:build rollback

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters per research.md decision 1.
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MiB
	argon2Threads = 4
	argon2SaltLen = 16
	argon2KeyLen  = 32
)

var (
	// ErrInvalidHash is returned when a password hash string is malformed.
	ErrInvalidHash = errors.New("invalid password hash format")
)

// HashPassword derives an Argon2id hash for the given password using a fresh
// random salt, encoded in PHC format:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<base64-salt>$<base64-hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	// Base64 without padding (matches PHC encoding used by libsodium etc.)
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads, b64Salt, b64Hash)

	return encoded, nil
}

// VerifyPassword checks a password against a stored PHC-encoded Argon2id hash.
// It re-derives the hash with the hash's own parameters and compares in constant time.
func VerifyPassword(encodedHash, password string) (bool, error) {
	params, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}

	candidate := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, params.keyLen)

	return subtle.ConstantTimeCompare(hash, candidate) == 1, nil
}

type argon2Params struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
}

func decodeHash(encoded string) (*argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return nil, nil, nil, fmt.Errorf("%w: unsupported argon2 version %d", ErrInvalidHash, version)
	}

	p := &argon2Params{}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return nil, nil, nil, ErrInvalidHash
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return nil, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	hash, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	p.keyLen = uint32(len(hash))
	return p, salt, hash, nil
}
