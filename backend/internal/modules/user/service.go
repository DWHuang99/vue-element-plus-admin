package user

import (
	"context"
	"database/sql"
	"errors"
	"time"
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
	IsActive    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type UserRepositoryReader interface {
	GetUserByID(ctx context.Context, userID int64) (*CurrentUser, error)
}

type UserService struct {
	// Add any dependencies or fields needed for the service
	repository UserRepositoryReader
}

func NewService(repository UserRepositoryReader) *UserService {
	return &UserService{
		repository: repository,
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
	return user, nil
}
