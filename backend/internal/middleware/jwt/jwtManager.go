package jwtservice

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"strconv"
	"time"
	"vue-element-plus-admin/backend/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

var ErrSigningUnavailable = errors.New("JWT signing is unavailable")

type JWTManager struct {
	privateKey *rsa.PrivateKey
	keyfunc    jwt.Keyfunc
	keyID      string
	issuer     string
	audience   string
	ttl        time.Duration
	RefreshTTL time.Duration
}

// NewJWTManager creates an issuer that signs with an RSA private key and verifies
// with the matching public key. Resource services that only verify tokens should
// use NewJWTVerifier with a cached remote JWKS key function.
func NewJWTManager(
	privateKey *rsa.PrivateKey,
	publicKey *rsa.PublicKey,
	keyID string,
	issuer string,
	audience string,
	ttl time.Duration,
	refreshTTL time.Duration,
) *JWTManager {
	return &JWTManager{
		privateKey: privateKey,
		keyfunc:    localRSAKeyfunc(publicKey, keyID),
		keyID:      keyID,
		issuer:     issuer,
		audience:   audience,
		ttl:        ttl,
		RefreshTTL: refreshTTL,
	}
}

// NewJWTVerifier creates a verification-only manager. keyfunc should be created
// once at application startup and reused for every request.
func NewJWTVerifier(keyfunc jwt.Keyfunc, issuer string, audience string) *JWTManager {
	return &JWTManager{keyfunc: requireKIDKeyfunc(keyfunc), issuer: issuer, audience: audience}
}

func CreateJWTManager(jwtConfig *config.JwtConfig) *JWTManager {
	return NewJWTManager(
		jwtConfig.PrivateKey,
		jwtConfig.PublicKey,
		jwtConfig.KeyID,
		jwtConfig.Issuer,
		jwtConfig.Audience,
		time.Duration(jwtConfig.AccessTokenExpireMs)*time.Millisecond,
		time.Duration(jwtConfig.RefreshTokenExpireMs)*time.Millisecond,
	)
}

func localRSAKeyfunc(publicKey *rsa.PublicKey, keyID string) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, fmt.Errorf("%w: missing kid", ErrInvalidToken)
		}
		if kid != keyID {
			return nil, fmt.Errorf("%w: unknown kid", ErrInvalidToken)
		}
		return publicKey, nil
	}
}

func requireKIDKeyfunc(keyfunc jwt.Keyfunc) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, fmt.Errorf("%w: missing kid", ErrInvalidToken)
		}
		return keyfunc(token)
	}
}

func (m *JWTManager) GenerateToken(userID int64, role []string) (string, error) {
	return m.GenerateTokenWithPermissions(userID, role, nil)
}

func (m *JWTManager) GenerateTokenWithPermissions(
	userID int64,
	role []string,
	permissions []string,
) (string, error) {
	if m.privateKey == nil {
		return "", ErrSigningUnavailable
	}

	now := time.Now()
	claims := Claims{
		Role:        role,
		Permissions: permissions,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   strconv.FormatInt(userID, 10),
			Audience:  jwt.ClaimStrings{m.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = m.keyID

	return token.SignedString(m.privateKey)
}

func (m *JWTManager) ParseToken(tokenString string) (*Claims, error) {
	if m.keyfunc == nil {
		return nil, ErrInvalidToken
	}

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		m.keyfunc,
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithAudience(m.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
