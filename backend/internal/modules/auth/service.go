package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	db "vue-element-plus-admin/backend/internal/database/iam/generated"
	"vue-element-plus-admin/backend/internal/dto/request"
	casbinrbac "vue-element-plus-admin/backend/internal/middleware/casbin"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"
	"vue-element-plus-admin/backend/internal/modules/user"
	"vue-element-plus-admin/backend/internal/security"

	"github.com/casbin/casbin/v3"
	"github.com/redis/go-redis/v9"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidRequest      = errors.New("invalid request")
	ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")
	ErrUserExists          = errors.New("username already exists")
	ErrUserNotFound        = errors.New("user not found")
	ErrUserDisabled        = errors.New("user is disabled")
	ErrDefaultRoleMissing  = errors.New("default registration role is unavailable")
)

const defaultRegistrationRoleCode = "user"

type AuthService struct {
	userRepository *user.UserRepository
	jwtmanager     *jwtservice.JWTManager
	redisClient    *redis.Client
	enforcer       *casbin.SyncedEnforcer
}

func NewService(
	userRepository *user.UserRepository,
	jwtmanager *jwtservice.JWTManager,
	redisClient *redis.Client,
	enforcer *casbin.SyncedEnforcer,
) *AuthService {
	return &AuthService{
		userRepository: userRepository,
		jwtmanager:     jwtmanager,
		redisClient:    redisClient,
		enforcer:       enforcer,
	}
}

func (s *AuthService) Login(ctx context.Context, loginreq request.LoginRequest) (string, string, bool, error) {
	userAuth, err := s.userRepository.GetUserAuthByUsername(ctx, loginreq.Username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}

	if security.Verify(loginreq.Password, userAuth.PasswordHash) {
		accessToken, refreshToken, err := s.issueTokensForUser(ctx, userAuth.ID)
		return accessToken, refreshToken, true, err
	}

	return "", "", false, nil

}

// LoginOIDC 在 OIDC 身份完成验证并映射到本地用户后建立本系统登录状态。
// userID 必须来自服务端验证后的 issuer + subject 绑定，不能来自客户端参数。
func (s *AuthService) LoginOIDC(ctx context.Context, userID int64) (string, string, error) {
	if userID <= 0 {
		return "", "", ErrUserNotFound
	}

	currentUser, err := s.userRepository.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrUserNotFound
		}
		return "", "", err
	}
	if !currentUser.IsActive {
		return "", "", ErrUserDisabled
	}

	userSubject := casbinrbac.UserSubject(currentUser.ID)
	if _, err := s.enforcer.DeleteRolesForUser(userSubject); err != nil {
		return "", "", err
	}
	if _, err := s.enforcer.AddRoleForUser(userSubject, casbinrbac.RoleSubject(currentUser.RoleCode)); err != nil {
		return "", "", err
	}

	return s.issueTokensForUser(ctx, userID)
}

func (s *AuthService) issueTokensForUser(ctx context.Context, userID int64) (string, string, error) {
	if userID <= 0 {
		return "", "", ErrUserNotFound
	}

	userAuth, err := s.userRepository.GetUserAuthByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrUserNotFound
		}
		return "", "", err
	}
	if !userAuth.IsActive {
		return "", "", ErrUserDisabled
	}

	roles, permissions, err := s.authorizationForUser(userAuth.ID)
	if err != nil {
		return "", "", err
	}
	accessToken, err := s.jwtmanager.GenerateTokenWithPermissions(
		userAuth.ID, roles, permissions,
	)
	if err != nil {
		return "", "", err
	}
	refreshToken, err := rdb.CreateRefreshToken(
		s.redisClient,
		ctx,
		userAuth.ID,
		s.jwtmanager.RefreshTTL,
	)
	if err != nil {
		return "", "", err
	}

	return accessToken, refreshToken, nil
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (string, string, error) {
	newRefreshToken, userID, err := rdb.RotateRefreshToken(s.redisClient, ctx, refreshToken, s.jwtmanager.RefreshTTL)
	if errors.Is(err, rdb.ErrRefreshTokenNotFound) {
		return "", "", ErrInvalidRefreshToken
	}
	if err != nil {
		return "", "", err
	}

	userAuth, err := s.userRepository.GetUserAuthByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrInvalidRefreshToken
		}
		return "", "", err
	}
	if !userAuth.IsActive {
		return "", "", ErrUserDisabled
	}
	roles, permissions, err := s.authorizationForUser(userAuth.ID)
	if err != nil {
		return "", "", err
	}

	newAccessToken, err := s.jwtmanager.GenerateTokenWithPermissions(
		userAuth.ID, roles, permissions,
	)
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

	user, err := s.userRepository.AddUser(
		ctx,
		registerRequest.Username,
		passwordHash,
		defaultRegistrationRoleCode,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDefaultRoleMissing
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrUserExists
		}
		return nil, err
	}
	userSubject := casbinrbac.UserSubject(user.ID)
	if _, err := s.enforcer.DeleteRolesForUser(userSubject); err != nil {
		return nil, err
	}
	if _, err := s.enforcer.AddRoleForUser(userSubject, casbinrbac.RoleSubject(defaultRegistrationRoleCode)); err != nil {
		return nil, err
	}

	return user, nil
}

func (s *AuthService) authorizationForUser(userID int64) ([]string, []string, error) {
	subject := casbinrbac.UserSubject(userID)
	roleSubjects, err := s.enforcer.GetImplicitRolesForUser(subject)
	if err != nil {
		return nil, nil, err
	}
	permissionRules, err := s.enforcer.GetImplicitPermissionsForUser(subject)
	if err != nil {
		return nil, nil, err
	}
	return casbinrbac.RoleCodes(roleSubjects), casbinrbac.PermissionCodes(permissionRules), nil
}

func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	return rdb.DeleteRefreshToken(s.redisClient, ctx, refreshToken)
}
