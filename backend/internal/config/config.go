package config

import (
	"os"
	"strconv"
)

type DbConfig struct {
	DatabaseURL string
	Dbtype      string
}

type RedisConfig struct {
	Address  string
	Password string
	DB       int
}

type CookieConfig struct {
	Secure bool
}

func getEnv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

func LoadDbConfig() *DbConfig {
	// Implement the logic to load the database configuration here
	return &DbConfig{
		DatabaseURL: getEnv("DATABASE_URL", "postgres://vue_admin:vue_admin_dev@postgres:5432/vue_admin?sslmode=disable"),
		Dbtype:      getEnv("DB_TYPE", "pgx"),
	}
}

func LoadRedisConfig() *RedisConfig {
	db, err := strconv.Atoi(getEnv("REDIS_DB", "0"))
	if err != nil {
		db = 0
	}

	return &RedisConfig{
		Address:  getEnv("REDIS_ADDR", "localhost:6379"),
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       db,
	}
}

func LoadCookieConfig() *CookieConfig {
	secure, err := strconv.ParseBool(getEnv("COOKIE_SECURE", "false"))
	if err != nil {
		secure = false
	}

	return &CookieConfig{Secure: secure}
}

type JwtConfig struct {
	JwtSecret               string
	JwtAccessTokenExpireMs  int64
	JwtRefreshTokenExpireMs int64
}

func LoadJwtConfig() *JwtConfig {
	expireMs, err := strconv.ParseInt(getEnv("JWT_ACCESS_TOKEN_EXPIRE_MS", "900000"), 10, 64)
	if err != nil {
		expireMs = 900000 // default to 15 minutes
	}
	refreshExpireMs, err := strconv.ParseInt(getEnv("JWT_REFRESH_TOKEN_EXPIRE_MS", "604800000"), 10, 64)
	if err != nil {
		refreshExpireMs = 604800000 // default to 7 days
	}

	return &JwtConfig{
		JwtSecret:               getEnv("JWT_SECRET", "default_secret"),
		JwtAccessTokenExpireMs:  expireMs,
		JwtRefreshTokenExpireMs: refreshExpireMs,
	}
}
