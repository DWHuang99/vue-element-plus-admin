package auth

import (
	"context"
	"errors"
	"strings"
	"time"
	db "vue-element-plus-admin/backend/internal/database/generated"
	"vue-element-plus-admin/backend/internal/dto/request"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"
	"vue-element-plus-admin/backend/internal/security"

	"github.com/redis/go-redis/v9"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidRequest      = errors.New("invalid request")
	ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")
	ErrUserExists          = errors.New("username already exists")
	ErrUserDisabled        = errors.New("user is disabled")
)

type AuthService struct {
	repository    *AuthRepository
	jwtmanager    *jwtservice.JWTManager
	refreshTokens RefreshTokenStore
}

type RefreshTokenStore interface {
	Create(ctx context.Context, username string, ttl time.Duration) (string, error)
	Rotate(ctx context.Context, refreshToken string, ttl time.Duration) (string, string, error)
	Delete(ctx context.Context, refreshToken string) error
}

type redisRefreshTokenStore struct {
	client *redis.Client
}

func (s *redisRefreshTokenStore) Create(ctx context.Context, username string, ttl time.Duration) (string, error) {
	return rdb.CreateRefreshToken(s.client, ctx, username, ttl)
}

func (s *redisRefreshTokenStore) Rotate(ctx context.Context, refreshToken string, ttl time.Duration) (string, string, error) {
	return rdb.RotateRefreshToken(s.client, ctx, refreshToken, ttl)
}

func (s *redisRefreshTokenStore) Delete(ctx context.Context, refreshToken string) error {
	return rdb.DeleteRefreshToken(s.client, ctx, refreshToken)
}

func NewService(repository *AuthRepository, jwtmanager *jwtservice.JWTManager, redisClient *redis.Client) *AuthService {
	return &AuthService{
		repository:    repository,
		jwtmanager:    jwtmanager,
		refreshTokens: &redisRefreshTokenStore{client: redisClient},
	}
}

func (s *AuthService) Login(ctx context.Context, loginreq request.LoginRequest) (string, string, bool, error) {
	userAuth, err := s.repository.GetUserAuthByUsername(ctx, loginreq.Username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}

	if security.Verify(loginreq.Password, userAuth.PasswordHash) {
		if !userAuth.IsActive {
			return "", "", true, ErrUserDisabled
		}
		accessToken, err := s.jwtmanager.GenerateToken(loginreq.Username, []string{userAuth.RoleCode})
		if err != nil {
			return "", "", true, err
		}
		refreshToken, err := s.refreshTokens.Create(ctx, loginreq.Username, s.jwtmanager.RefreshTTL)
		if err != nil {
			return "", "", true, err
		}
		return accessToken, refreshToken, true, nil
	}

	return "", "", false, nil

}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (string, string, error) {
	newRefreshToken, username, err := s.refreshTokens.Rotate(ctx, refreshToken, s.jwtmanager.RefreshTTL)
	if errors.Is(err, rdb.ErrRefreshTokenNotFound) {
		return "", "", ErrInvalidRefreshToken
	}
	if err != nil {
		return "", "", err
	}

	userAuth, err := s.repository.GetUserAuthByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrInvalidRefreshToken
		}
		return "", "", err
	}
	if !userAuth.IsActive {
		return "", "", ErrUserDisabled
	}

	newAccessToken, err := s.jwtmanager.GenerateToken(username, []string{userAuth.RoleCode})
	if err != nil {
		return "", "", err
	}

	return newAccessToken, newRefreshToken, nil
}

func (s *AuthService) RefreshTTL() time.Duration {
	return s.jwtmanager.RefreshTTL
}

func (s *AuthService) Register(ctx context.Context, registerRequest request.RegisterRequest) (*db.User, error) {
	if strings.TrimSpace(registerRequest.Username) == "" ||
		registerRequest.Password == "" ||
		registerRequest.Password != registerRequest.CheckPassword ||
		strings.TrimSpace(registerRequest.Code) == "" ||
		!registerRequest.IAgree {
		return nil, ErrInvalidRequest
	}

	passwordHash, err := security.Hash(registerRequest.Password)
	if err != nil {
		return nil, err
	}

	newUser := db.AddUserParams{
		Username:     registerRequest.Username,
		PasswordHash: passwordHash,
		RoleID:       2,
	}

	user, err := s.repository.AddUser(ctx, newUser)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrUserExists
		}
		return nil, err
	}

	return user, nil
}

func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	return s.refreshTokens.Delete(ctx, refreshToken)
}
