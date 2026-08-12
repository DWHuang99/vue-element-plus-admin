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

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidRequest      = errors.New("invalid request")
	ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")
	ErrUserExists          = errors.New("username already exists")
)

type AuthService struct {
	// Add any dependencies or fields needed for the service
	repository  *AuthRepository
	jwtmanager  *jwtservice.JWTManager
	redisClient *redis.Client
}

func NewService(repository *AuthRepository, jwtmanager *jwtservice.JWTManager, rdb *redis.Client) *AuthService {
	return &AuthService{
		repository:  repository,
		jwtmanager:  jwtmanager,
		redisClient: rdb,
	}
}

func (s *AuthService) Login(ctx context.Context, loginreq request.LoginRequest) (string, string, bool, error) {
	hash, err := s.repository.GetUserPassword(ctx, loginreq.Username)
	if err != nil {
		return "", "", false, err
	}

	if security.Verify(loginreq.Password, hash) {
		accessToken, err := s.jwtmanager.GenerateToken(loginreq.Username, []string{"user"})
		if err != nil {
			return "", "", true, err
		}
		refreshToken, err := rdb.CreateRefreshToken(s.redisClient, ctx, loginreq.Username, s.jwtmanager.RefreshTTL)
		if err != nil {
			return "", "", true, err
		}
		return accessToken, refreshToken, true, nil
	}

	return "", "", false, nil

}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (string, string, error) {
	newRefreshToken, username, err := rdb.RotateRefreshToken(s.redisClient, ctx, refreshToken, s.jwtmanager.RefreshTTL)
	if errors.Is(err, rdb.ErrRefreshTokenNotFound) {
		return "", "", ErrInvalidRefreshToken
	}
	if err != nil {
		return "", "", err
	}

	newAccessToken, err := s.jwtmanager.GenerateToken(username, []string{"user"})
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
