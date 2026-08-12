package jwtservice

import (
	"time"

	"vue-element-plus-admin/backend/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

type JWTManager struct {
	secret     []byte
	issuer     string
	ttl        time.Duration
	RefreshTTL time.Duration
}

func NewJWTManager(secret string, issuer string, ttl time.Duration, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		secret:     []byte(secret),
		issuer:     issuer,
		ttl:        ttl,
		RefreshTTL: refreshTTL,
	}
}

func CreateJWTManager(jwtConfig *config.JwtConfig) *JWTManager {
	// Load JWT configuration from environment variables
	return &JWTManager{
		secret:     []byte(jwtConfig.JwtSecret),
		issuer:     "vue-element-plus-admin",
		ttl:        time.Duration(jwtConfig.JwtAccessTokenExpireMs) * time.Millisecond,
		RefreshTTL: time.Duration(jwtConfig.JwtRefreshTokenExpireMs) * time.Millisecond,
	}
}

func (m *JWTManager) GenerateToken(
	username string,
	role []string,
) (string, error) {
	now := time.Now()

	claims := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}

	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		claims,
	)

	return token.SignedString(m.secret)
}

func (m *JWTManager) ParseToken(
	tokenString string,
) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,

		func(token *jwt.Token) (any, error) {
			return m.secret, nil
		},

		// 只接受 HS256
		jwt.WithValidMethods([]string{
			jwt.SigningMethodHS256.Alg(),
		}),

		// 要求 iss 必须是指定值
		jwt.WithIssuer(m.issuer),

		// 要求 JWT 必须包含 exp
		jwt.WithExpirationRequired(),
	)

	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
