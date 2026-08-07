package auth

// Gin context keys for authenticated request state.
// Defined here (not in middleware) so the auth package and middleware can
// share them without an import cycle.
const (
	// ContextAuthUser holds the *AuthUser injected by the auth middleware.
	ContextAuthUser = "auth_user"
	// ContextTokenHash holds the SHA-256 hash of the presented token.
	ContextTokenHash = "auth_token_hash"
)
