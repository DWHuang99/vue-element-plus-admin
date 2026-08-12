package auth

import (
	"errors"
	"net/http"
	"vue-element-plus-admin/backend/internal/dto/request"
	"vue-element-plus-admin/backend/internal/dto/response"

	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	service      *AuthService
	cookieSecure bool
}

func NewAuthHandler(service *AuthService, cookieSecure bool) *AuthHandler {
	return &AuthHandler{
		service:      service,
		cookieSecure: cookieSecure,
	}
}

const refreshCookiePath = "/api/v1/auth/refresh"

func (h *AuthHandler) setRefreshCookie(c *gin.Context, refreshToken string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(
		"refresh_token",
		refreshToken,
		int(h.service.RefreshTTL().Seconds()),
		refreshCookiePath,
		"",
		h.cookieSecure,
		true,
	)
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req request.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, 10001, "invalid request")
		return
	}
	if accessToken, refreshToken, exist, err := h.service.Login(c.Request.Context(), req); err == nil && exist {
		h.setRefreshCookie(c, refreshToken)
		response.Success(c, gin.H{
			"message":     "Login successful",
			"accessToken": accessToken,
			"exist":       exist,
		})
	} else {
		if exist {
			response.Error(c, http.StatusInternalServerError, 1, "accessToken/refreshToken generation failed")
		} else {
			response.Error(c, http.StatusUnauthorized, 1, "Login failed: password incorrect")
		}
	}
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req request.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, 10001, "invalid request")
		return
	}

	_, err := h.service.Register(c.Request.Context(), req)
	switch {
	case err == nil:
		response.SuccessWithStatus(c, http.StatusCreated, nil, "register successful")
	case errors.Is(err, ErrInvalidRequest):
		response.Error(c, http.StatusBadRequest, 10001, err.Error())
	case errors.Is(err, ErrUserExists):
		response.Error(c, http.StatusConflict, 10002, err.Error())
	default:
		response.Error(c, http.StatusInternalServerError, 10500, "internal server error")
	}
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	refreshToken, err := c.Cookie("refresh_token")
	if err != nil {
		response.Error(c, http.StatusUnauthorized, 1, "Refresh failed: missing refresh token")
		return
	}

	accessToken, newRefreshToken, err := h.service.Refresh(c.Request.Context(), refreshToken)
	switch {
	case err == nil:
		h.setRefreshCookie(c, newRefreshToken)
		response.Success(c, gin.H{"accessToken": accessToken})
	case errors.Is(err, ErrInvalidRefreshToken):
		response.Error(c, http.StatusUnauthorized, 1, "Refresh failed: invalid refresh token")
	default:
		response.Error(c, http.StatusInternalServerError, 10500, "internal server error")
	}
}
