package rdb

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const oidcStateKeyPrefix = "oidc:flow:"

func oidcStateKey(state string) string {
	return oidcStateKeyPrefix + state
}

func SetState(client *redis.Client, ctx context.Context, state string, data []byte, ttl time.Duration) error {
	if err := client.Set(ctx, oidcStateKey(state), data, ttl).Err(); err != nil {
		return err
	}
	return nil
}

func DeleteState(client *redis.Client, ctx context.Context, state string) (string, error) {
	result, err := client.GetDel(ctx, oidcStateKey(state)).Result()
	if err != nil {
		return "", err
	}
	return result, nil
}
