package config

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
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

type ServiceConfig struct {
	DepartmentGRPCTarget string
	DepartmentGRPCAddr   string
	IAMGRPCTarget        string
	IAMGRPCAddr          string
	GRPCTimeout          time.Duration
}

type JWTVerifierConfig struct {
	JWKSURL  string
	Issuer   string
	Audience string
}

type OIDCConfig struct {
	Enabled             bool
	Issuer              string
	ClientID            string
	ClientSecret        string
	RedirectURL         string
	FrontendRedirectURL string
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

func LoadServiceConfig() (*ServiceConfig, error) {
	timeoutMs, err := positiveInt64Env("SERVICE_GRPC_TIMEOUT_MS", 3000)
	if err != nil {
		return nil, err
	}
	return &ServiceConfig{
		DepartmentGRPCTarget: getEnv("DEPARTMENT_GRPC_TARGET", "localhost:50051"),
		DepartmentGRPCAddr:   getEnv("DEPARTMENT_GRPC_ADDR", ":50051"),
		IAMGRPCTarget:        getEnv("IAM_GRPC_TARGET", "localhost:50051"),
		IAMGRPCAddr:          getEnv("IAM_GRPC_ADDR", ":50051"),
		GRPCTimeout:          time.Duration(timeoutMs) * time.Millisecond,
	}, nil
}

func LoadJWTVerifierConfig() *JWTVerifierConfig {
	return &JWTVerifierConfig{
		JWKSURL:  getEnv("JWT_JWKS_URL", "http://localhost:8081/.well-known/jwks.json"),
		Issuer:   getEnv("JWT_ISSUER", "vue-element-plus-admin"),
		Audience: getEnv("JWT_AUDIENCE", "vue-element-plus-admin-api"),
	}
}

type JwtConfig struct {
	PrivateKeyPath       string
	PublicKeyPath        string
	PrivateKey           *rsa.PrivateKey
	PublicKey            *rsa.PublicKey
	KeyID                string
	Issuer               string
	Audience             string
	AccessTokenExpireMs  int64
	RefreshTokenExpireMs int64
}

func LoadJwtConfig() (*JwtConfig, error) {
	expireMs, err := positiveInt64Env("JWT_ACCESS_TOKEN_EXPIRE_MS", 900000)
	if err != nil {
		return nil, err
	}
	refreshExpireMs, err := positiveInt64Env("JWT_REFRESH_TOKEN_EXPIRE_MS", 604800000)
	if err != nil {
		return nil, err
	}

	privateKeyPath := getEnv("JWT_PRIVATE_KEY_PATH", "../private.pem")
	publicKeyPath := getEnv("JWT_PUBLIC_KEY_PATH", "../public.pem")
	privateKey, err := loadRSAPrivateKey(privateKeyPath)
	if err != nil {
		return nil, err
	}
	if err := privateKey.Validate(); err != nil {
		return nil, fmt.Errorf("validate JWT private key %q: %w", privateKeyPath, err)
	}
	if privateKey.N.BitLen() < 2048 {
		return nil, fmt.Errorf("JWT RSA key must be at least 2048 bits")
	}
	publicKey, err := loadRSAPublicKey(publicKeyPath)
	if err != nil {
		return nil, err
	}
	if privateKey.PublicKey.E != publicKey.E || privateKey.PublicKey.N.Cmp(publicKey.N) != 0 {
		return nil, errors.New("JWT private and public keys do not match")
	}

	return &JwtConfig{
		PrivateKeyPath:       privateKeyPath,
		PublicKeyPath:        publicKeyPath,
		PrivateKey:           privateKey,
		PublicKey:            publicKey,
		KeyID:                getEnv("JWT_KEY_ID", "key-2026-08"),
		Issuer:               getEnv("JWT_ISSUER", "vue-element-plus-admin"),
		Audience:             getEnv("JWT_AUDIENCE", "vue-element-plus-admin-api"),
		AccessTokenExpireMs:  expireMs,
		RefreshTokenExpireMs: refreshExpireMs,
	}, nil
}

func positiveInt64Env(key string, fallback int64) (int64, error) {
	value := getEnv(key, strconv.FormatInt(fallback, 10))
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read JWT private key %q: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("decode JWT private key %q: invalid PEM", path)
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key %q: %w", path, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parse JWT private key %q: key is not RSA", path)
	}
	return key, nil
}

func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read JWT public key %q: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("decode JWT public key %q: invalid PEM", path)
	}

	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if key, ok := parsed.(*rsa.PublicKey); ok {
			return key, nil
		}
	}
	if certificate, err := x509.ParseCertificate(block.Bytes); err == nil {
		if key, ok := certificate.PublicKey.(*rsa.PublicKey); ok {
			return key, nil
		}
	}

	return nil, fmt.Errorf("parse JWT public key %q: key is not a supported RSA public key", path)
}

func LoadOidcConfig() (*OIDCConfig, error) {
	enabled, err := strconv.ParseBool(getEnv("OIDC_ENABLED", "false"))
	if err != nil {
		return nil, errors.New("OIDC_ENABLED must be true or false")
	}
	configuration := &OIDCConfig{
		Enabled:             enabled,
		Issuer:              strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
		ClientID:            strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID")),
		ClientSecret:        strings.TrimSpace(os.Getenv("OIDC_CLIENT_SECRET")),
		RedirectURL:         strings.TrimSpace(os.Getenv("OIDC_REDIRECT_URL")),
		FrontendRedirectURL: strings.TrimSpace(os.Getenv("OIDC_FRONTEND_REDIRECT_URL")),
	}
	if !configuration.Enabled {
		return configuration, nil
	}

	required := []struct {
		key   string
		value string
	}{
		{key: "OIDC_ISSUER", value: configuration.Issuer},
		{key: "OIDC_CLIENT_ID", value: configuration.ClientID},
		{key: "OIDC_CLIENT_SECRET", value: configuration.ClientSecret},
		{key: "OIDC_REDIRECT_URL", value: configuration.RedirectURL},
		{key: "OIDC_FRONTEND_REDIRECT_URL", value: configuration.FrontendRedirectURL},
	}
	for _, item := range required {
		if item.value == "" {
			return nil, fmt.Errorf("%s is required", item.key)
		}
	}

	URLs := []struct {
		key   string
		value string
	}{
		{key: "OIDC_ISSUER", value: configuration.Issuer},
		{key: "OIDC_REDIRECT_URL", value: configuration.RedirectURL},
		{key: "OIDC_FRONTEND_REDIRECT_URL", value: configuration.FrontendRedirectURL},
	}
	for _, item := range URLs {
		parsed, err := url.Parse(item.value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("%s must be an absolute HTTP(S) URL", item.key)
		}
	}

	return configuration, nil
}
