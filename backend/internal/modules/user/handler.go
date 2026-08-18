package user

import (
	"context"
	"errors"
	"net/http"

	"vue-element-plus-admin/backend/internal/dto/response"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	"vue-element-plus-admin/backend/internal/modules/permission"

	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	service     CurrentUserService
	menuService CurrentUserMenuService
}

type CurrentUserService interface {
	GetUserByID(ctx context.Context, userID int64) (*CurrentUser, error)
}

type CurrentUserMenuService interface {
	AuthorizedTree(ctx context.Context, roleCodes []string, administrator bool) ([]permission.MenuItem, error)
}

func NewUserHandler(service CurrentUserService, menuService CurrentUserMenuService) *UserHandler {
	return &UserHandler{service: service, menuService: menuService}
}

func (h *UserHandler) GetCurrentUserMenus(c *gin.Context) {
	userIDValue, exists := c.Get(jwtservice.UserIDContextKey)
	userID, ok := userIDValue.(int64)
	if !exists || !ok || userID <= 0 {
		response.Error(c, http.StatusUnauthorized, 401, "invalid user identity")
		return
	}
	if h.menuService == nil {
		response.Error(c, http.StatusInternalServerError, 10500, "menu service unavailable")
		return
	}

	currentUser, err := h.service.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrUserDisabled) {
			response.Error(c, http.StatusForbidden, 40301, "user is disabled")
			return
		}
		if errors.Is(err, ErrUserNotExists) {
			response.Error(c, http.StatusNotFound, 40401, "user not found")
			return
		}
		response.Error(c, http.StatusInternalServerError, 10500, "internal server error")
		return
	}

	administrator := false
	for _, permissionCode := range currentUser.Permissions {
		if permissionCode == "*" || permissionCode == "*:*:*" || permissionCode == "*.*.*" {
			administrator = true
			break
		}
	}
	menus, err := h.menuService.AuthorizedTree(c.Request.Context(), currentUser.RoleCodes, administrator)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, 10500, "failed to load user menus")
		return
	}
	response.Success(c, gin.H{"list": menus})
}

func ToUserInfoResponse(user *CurrentUser) *response.UserInfo {
	return &response.UserInfo{
		ID:          user.ID,
		Username:    user.Username,
		RoleID:      user.RoleID,
		RoleCode:    user.RoleCode,
		RoleName:    user.RoleName,
		Roles:       user.RoleCodes,
		Permissions: user.Permissions,
		IsActive:    user.IsActive,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
	}
}

func (h *UserHandler) GetCurrentUser(c *gin.Context) {
	userIDValue, exists := c.Get(jwtservice.UserIDContextKey)
	if !exists {
		response.Error(c, http.StatusUnauthorized, 401, "user identity not found")
		return
	}

	userID, ok := userIDValue.(int64)
	if !ok || userID <= 0 {
		response.Error(c, http.StatusUnauthorized, 401, "invalid user identity")
		return
	}

	user, err := h.service.GetUserByID(c.Request.Context(), userID)
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
