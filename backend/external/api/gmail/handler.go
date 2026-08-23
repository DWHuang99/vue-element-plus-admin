package gmailapi

import (
	"context"
	"errors"
	"net/http"
	"time"
	"vue-element-plus-admin/backend/internal/dto/response"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

const gmailOperationTimeout = 18 * time.Second

type GmailHandler struct {
	service *GmailService
}

func NewGmailHandler(service *GmailService) *GmailHandler {
	return &GmailHandler{service: service}
}

func IsUnauthorized(err error) bool {
	var gmailErr *GmailAPIError

	return errors.As(err, &gmailErr) &&
		gmailErr.StatusCode == http.StatusUnauthorized
}

func (h *GmailHandler) ListUnreadMessages(c *gin.Context) {
	requestContext, cancel := context.WithTimeout(c.Request.Context(), gmailOperationTimeout)
	defer cancel()

	userIDValue, exists := c.Get(jwtservice.UserIDContextKey)
	userID, ok := userIDValue.(int64)
	if !exists || !ok || userID <= 0 {
		response.Error(c, http.StatusUnauthorized, 401, "invalid user identity")
		return
	}
	googleToken, err := h.service.GetToken(requestContext, userID)
	if err != nil {
		if writeGoogleAuthorizationError(c, err) {
			return
		}
		response.Error(c, http.StatusInternalServerError, 500, "failed to get Google token")
		return
	}

	messagelist, err := ListUnreadMessageDetails(requestContext, googleToken.AccessToken)
	if err != nil {
		if IsUnauthorized(err) {
			newToken, err := h.service.RefreshGoogleToken(requestContext, userID)
			if err != nil {
				if writeGoogleAuthorizationError(c, err) {
					return
				}
				response.Error(c, http.StatusBadGateway, 502, "failed to refresh Google token")
				return
			}
			messagelist, err = ListUnreadMessageDetails(requestContext, newToken.AccessToken)
			if err != nil {
				response.Error(c, http.StatusBadGateway, 502, "failed to list unread messages")
				return
			}
		} else {
			response.Error(c, http.StatusBadGateway, 502, "failed to list unread messages")
			return
		}
	}
	response.Success(c, messagelist)
}

func writeGoogleAuthorizationError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, ErrGoogleNotConnected):
		response.Error(c, http.StatusConflict, 40901, "Google integration is not connected")
		return true
	case errors.Is(err, ErrGoogleReconnectNeeded):
		response.Error(c, http.StatusConflict, 40902, "Google authorization must be renewed")
		return true
	default:
		return false
	}
}
