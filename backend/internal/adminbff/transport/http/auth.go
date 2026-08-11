// BFF auth handlers (T025): register/login/logout/me with wire parity.
//
// Register returns 201, login 200, both with the AuthTokenResponse payload;
// the user summary is re-read through GetIdentity so created_at is present
// (the IAM AuthSession carries only the Principal). Logout is idempotent and
// always answers 200 {"data":{}} — never an empty-body 204 (the frontend
// interceptor treats that as a failure). /auth/me is the T028 composition.
package http

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// Handler adapts the BFF service and the IAM/Organization ports to HTTP
// (protocol layer only; no adapters appear here).
type Handler struct {
	svc      *adminbff.Service
	auth     iam.AuthService
	identity iam.IdentityService
	roles    iam.RoleService
	depts    organization.DepartmentService
	logger   *slog.Logger
}

// NewHandler creates the BFF HTTP handler set.
func NewHandler(svc *adminbff.Service, auth iam.AuthService, identity iam.IdentityService,
	roles iam.RoleService, depts organization.DepartmentService, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, auth: auth, identity: identity, roles: roles, depts: depts, logger: logger}
}

// Register handles POST /api/v1/auth/register.
func (h *Handler) Register(c *gin.Context) {
	var req credentialsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, h.logger, iam.ErrInvalidInput)
		return
	}
	if fieldErrs := validateCredentials(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, ErrorEnvelope{Error: ErrorBodyDTO{
			Code: "AUTH_INVALID_INPUT", Message: "输入字段校验失败", FieldErrors: fieldErrs,
		}})
		return
	}

	session, err := h.auth.Register(c.Request.Context(), req.Username, req.Password,
		operationContext(c))
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	resp, err := h.tokenResponse(c, session)
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	h.logger.Info("user registered", "request_id", c.GetString(middleware.RequestIDHeader),
		"user_id", resp.User.ID)
	writeData(c, http.StatusCreated, resp)
}

// Login handles POST /api/v1/auth/login. Credential failures are uniform
// (401 AUTH_INVALID_CREDENTIALS) — no user-enumeration signal.
func (h *Handler) Login(c *gin.Context) {
	var req credentialsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, h.logger, iam.ErrInvalidInput)
		return
	}
	if fieldErrs := validateCredentials(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, ErrorEnvelope{Error: ErrorBodyDTO{
			Code: "AUTH_INVALID_INPUT", Message: "输入字段校验失败", FieldErrors: fieldErrs,
		}})
		return
	}

	session, err := h.auth.Login(c.Request.Context(), req.Username, req.Password,
		operationContext(c))
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	resp, err := h.tokenResponse(c, session)
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	h.logger.Info("user logged in", "request_id", c.GetString(middleware.RequestIDHeader),
		"user_id", resp.User.ID)
	writeData(c, http.StatusOK, resp)
}

// Logout handles POST /api/v1/auth/logout. Revoking an already-invalid token
// stays idempotent (200); a missing/malformed header is rejected earlier by
// BearerTokenFormat with 401.
func (h *Handler) Logout(c *gin.Context) {
	token := c.GetString(ContextRawToken)
	if token == "" {
		writeError(c, h.logger, iam.ErrInvalidToken)
		return
	}
	if err := h.auth.RevokeSession(c.Request.Context(), token, operationContext(c)); err != nil {
		writeError(c, h.logger, err)
		return
	}
	h.logger.Info("user logged out", "request_id", c.GetString(middleware.RequestIDHeader))
	writeData(c, http.StatusOK, EmptyData{})
}

// Me handles GET /api/v1/auth/me: the T028 profile composition.
func (h *Handler) Me(c *gin.Context) {
	principal, ok := principalFromContext(c)
	if !ok {
		writeError(c, h.logger, adminbff.ErrUnauthorized)
		return
	}
	me, err := h.svc.Me(c.Request.Context(), principal.UserID,
		c.GetString(middleware.RequestIDHeader))
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	writeData(c, http.StatusOK, MeResponse{User: toUserProfileDTO(me.User)})
}

// tokenResponse maps the issued session plus a fresh identity read into the
// public token payload. The raw token leaves the request scope immediately.
func (h *Handler) tokenResponse(c *gin.Context, session iam.AuthSession) (AuthTokenResponse, error) {
	identity, err := h.identity.GetIdentity(c.Request.Context(), session.User.UserID)
	if err != nil {
		return AuthTokenResponse{}, err
	}
	return AuthTokenResponse{
		Token:     session.Token,
		TokenType: session.TokenType,
		ExpiresIn: int64(session.ExpiresIn.Seconds()),
		User: AuthUserDTO{
			ID:        identity.ID,
			Username:  identity.Username,
			CreatedAt: identity.CreatedAt,
		},
	}, nil
}

// operationContext builds the IAM port context from the validated request ID
// (T073). Auth operations run pre-principal, so the actor stays 0.
func operationContext(c *gin.Context) iam.OperationContext {
	return iamRequestContext(c)
}

// toUserProfileDTO maps the composed profile to the frozen public DTO.
func toUserProfileDTO(p adminbff.UserProfile) UserProfileDTO {
	roles := make([]RoleDTO, 0, len(p.Roles))
	for _, r := range p.Roles {
		roles = append(roles, RoleDTO{ID: r.ID, Name: r.Name, Code: r.Code})
	}
	dto := UserProfileDTO{
		ID:                   p.ID,
		Username:             p.Username,
		Account:              strOrEmpty(p.Account),
		Email:                strOrEmpty(p.Email),
		CreatedAt:            p.CreatedAt,
		Roles:                roles,
		EffectivePermissions: p.EffectivePermissions,
	}
	if p.Department != nil {
		dto.Department = &DepartmentDTO{ID: p.Department.ID, Name: p.Department.Name}
	}
	if dto.EffectivePermissions == nil {
		dto.EffectivePermissions = []string{}
	}
	return dto
}

// strOrEmpty renders a nullable string as a plain string (NULL -> ""),
// mirroring the legacy profile projection.
func strOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
