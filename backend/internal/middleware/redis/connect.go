package rdb

import (
	"context"
	"vue-element-plus-admin/backend/internal/config"

	"github.com/redis/go-redis/v9"
)

func ConnectRedis(ctx context.Context, redisConfig *config.RedisConfig) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisConfig.Address,
		Password: redisConfig.Password,
		DB:       redisConfig.DB,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		panic(err)
	}

	return rdb
}
