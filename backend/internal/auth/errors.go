//go:build rollback

package auth

import "errors"

// Sentinel errors returned by the auth service.
// The HTTP layer maps these to contract error codes (see contracts/auth-api.md):
//
//	ErrUsernameTaken      → AUTH_USERNAME_TAKEN      (409)
//	ErrInvalidCredentials → AUTH_INVALID_CREDENTIALS (401)
//	ErrInvalidToken       → AUTH_INVALID_TOKEN       (401)
//	ErrInvalidInput       → AUTH_INVALID_INPUT       (400)
var (
	// ErrUsernameTaken is returned when the requested username already exists.
	ErrUsernameTaken = errors.New("username already taken")
	// ErrInvalidCredentials is returned for login failures (uniform, anti-enumeration).
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidToken is returned for missing/invalid/expired/revoked session tokens.
	ErrInvalidToken = errors.New("invalid token")
	// ErrInvalidInput is returned for structurally invalid request fields.
	ErrInvalidInput = errors.New("invalid input")
)
