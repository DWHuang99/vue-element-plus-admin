package user

import (
	"context"
	"database/sql"
	"errors"
	"time"

	casbinrbac "vue-element-plus-admin/backend/internal/middleware/casbin"

	"github.com/casbin/casbin/v3"
)

var (
	ErrInvalidRequest     = errors.New("invalid request")
	ErrInvalidAccessToken = errors.New("invalid or expired access token")
	ErrUserNotExists      = errors.New("user does not exist")
	ErrUserDisabled       = errors.New("user is disabled")
)

type CurrentUser struct {
	ID          int64
	Username    string
	RoleID      int64
	RoleCode    string
	RoleName    string
	Permissions []string
	RoleCodes   []string
	IsActive    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type UserRepositoryReader interface {
	GetUserByID(ctx context.Context, userID int64) (*CurrentUser, error)
}

type UserService struct {
	repository UserRepositoryReader
	enforcer   *casbin.SyncedEnforcer
}

func NewService(repository UserRepositoryReader, enforcer *casbin.SyncedEnforcer) *UserService {
	return &UserService{
		repository: repository,
		enforcer:   enforcer,
	}
}

func (s *UserService) GetUserByID(ctx context.Context, userID int64) (*CurrentUser, error) {
	user, err := s.repository.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotExists
		}
		return nil, err
	}
	if !user.IsActive {
		return nil, ErrUserDisabled
	}
	subject := casbinrbac.UserSubject(user.ID)
	roleSubjects, err := s.enforcer.GetImplicitRolesForUser(subject)
	if err != nil {
		return nil, err
	}
	permissionRules, err := s.enforcer.GetImplicitPermissionsForUser(subject)
	if err != nil {
		return nil, err
	}
	user.Permissions = casbinrbac.PermissionCodes(permissionRules)
	user.RoleCodes = casbinrbac.RoleCodes(roleSubjects)
	return user, nil
}
