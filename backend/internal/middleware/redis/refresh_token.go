package rdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrRefreshTokenNotFound = errors.New("refresh token not found")

const refreshTokenPrefix = "refresh_token:"

const rotateRefreshTokenScript = `
local user_id = redis.call('GET', KEYS[1])
if not user_id then
    return false
end

redis.call('DEL', KEYS[1])
redis.call('SET', KEYS[2], user_id, 'PX', ARGV[1])
return user_id
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

func CreateRefreshToken(client *redis.Client, ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	refreshToken, err := newRefreshToken()
	if err != nil {
		return "", err
	}

	if err := client.Set(ctx, refreshTokenKey(refreshToken), strconv.FormatInt(userID, 10), ttl).Err(); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}

	return refreshToken, nil
}

func RotateRefreshToken(client *redis.Client, ctx context.Context, oldRefreshToken string, ttl time.Duration) (string, int64, error) {
	newToken, err := newRefreshToken()
	if err != nil {
		return "", 0, err
	}

	userIDValue, err := client.Eval(
		ctx,
		rotateRefreshTokenScript,
		[]string{refreshTokenKey(oldRefreshToken), refreshTokenKey(newToken)},
		ttl.Milliseconds(),
	).Text()
	if errors.Is(err, redis.Nil) {
		return "", 0, ErrRefreshTokenNotFound
	}
	if err != nil {
		return "", 0, fmt.Errorf("rotate refresh token: %w", err)
	}

	userID, err := strconv.ParseInt(userIDValue, 10, 64)
	if err != nil || userID <= 0 {
		_ = client.Del(ctx, refreshTokenKey(newToken)).Err()
		return "", 0, ErrRefreshTokenNotFound
	}

	return newToken, userID, nil
}

func DeleteRefreshToken(client *redis.Client, ctx context.Context, refreshToken string) error {
	if err := client.Del(ctx, refreshTokenKey(refreshToken)).Err(); err != nil {
		return fmt.Errorf("delete refresh token: %w", err)
	}
	return nil
}
