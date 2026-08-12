package rdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrRefreshTokenNotFound = errors.New("refresh token not found")

const refreshTokenPrefix = "refresh_token:"

const rotateRefreshTokenScript = `
local username = redis.call('GET', KEYS[1])
if not username then
    return false
end

redis.call('DEL', KEYS[1])
redis.call('SET', KEYS[2], username, 'PX', ARGV[1])
return username
`

func newRefreshToken() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

func refreshTokenKey(refreshToken string) string {
	digest := sha256.Sum256([]byte(refreshToken))
	return refreshTokenPrefix + hex.EncodeToString(digest[:])
}

func CreateRefreshToken(client *redis.Client, ctx context.Context, username string, ttl time.Duration) (string, error) {
	refreshToken, err := newRefreshToken()
	if err != nil {
		return "", err
	}

	if err := client.Set(ctx, refreshTokenKey(refreshToken), username, ttl).Err(); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}

	return refreshToken, nil
}

func RotateRefreshToken(client *redis.Client, ctx context.Context, oldRefreshToken string, ttl time.Duration) (string, string, error) {
	newToken, err := newRefreshToken()
	if err != nil {
		return "", "", err
	}

	username, err := client.Eval(
		ctx,
		rotateRefreshTokenScript,
		[]string{refreshTokenKey(oldRefreshToken), refreshTokenKey(newToken)},
		ttl.Milliseconds(),
	).Text()
	if errors.Is(err, redis.Nil) {
		return "", "", ErrRefreshTokenNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("rotate refresh token: %w", err)
	}

	return newToken, username, nil
}
