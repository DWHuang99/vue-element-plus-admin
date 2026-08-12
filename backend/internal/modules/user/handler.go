package user

import (
	"context"
	"errors"
	"net/http"

	"vue-element-plus-admin/backend/internal/dto/response"

	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	service CurrentUserService
}

type CurrentUserService interface {
	GetUserByUsername(ctx context.Context, username string) (*CurrentUser, error)
}

func NewUserHandler(service CurrentUserService) *UserHandler {
	return &UserHandler{
		service: service,
	}
}

func ToUserInfoResponse(user *CurrentUser) *response.UserInfo {
	return &response.UserInfo{
		ID:          user.ID,
		Username:    user.Username,
		RoleID:      user.RoleID,
		RoleCode:    user.RoleCode,
		RoleName:    user.RoleName,
		Permissions: user.Permissions,
		IsActive:    user.IsActive,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
	}
}

func (h *UserHandler) GetCurrentUser(c *gin.Context) {
	usernameValue, exists := c.Get("username")
	if !exists {
		response.Error(c, http.StatusUnauthorized, 401, "user identity not found")
		return
	}

	username, ok := usernameValue.(string)
	if !ok || username == "" {
		response.Error(c, http.StatusUnauthorized, 401, "invalid user identity")
		return
	}

	user, err := h.service.GetUserByUsername(c.Request.Context(), username)
	if err != nil {
		if errors.Is(err, ErrUserNotExists) {
			response.Error(c, http.StatusNotFound, 40401, "user not found")
			return
		}
		if errors.Is(err, ErrUserDisabled) {
			response.Error(c, http.StatusForbidden, 40301, "user is disabled")
			return
		}
		response.Error(c, http.StatusInternalServerError, 10500, "internal server error")
		return
	}

	response.Success(c, ToUserInfoResponse(user))
}
