package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// Validation rules per research.md decision 9.
var (
	usernameRe     = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	minUsernameLen = 3
	maxUsernameLen = 32
	minPasswordLen = 8
	maxPasswordLen = 72
)

// Handler adapts the auth Service to HTTP (protocol layer only).
type Handler struct {
	svc    Service
	logger *slog.Logger
}

// NewHandler creates an auth HTTP handler.
func NewHandler(svc Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// credentialsRequest is the shared request body for register and login.
type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// fieldError describes a single validation failure.
type fieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorEnvelope is the uniform error response shape.
type errorEnvelope struct {
	Error struct {
		Code        string       `json:"code"`
		Message     string       `json:"message"`
		FieldErrors []fieldError `json:"field_errors,omitempty"`
	} `json:"error"`
}

// Register handles POST /api/v1/auth/register.
func (h *Handler) Register(c *gin.Context) {
	var req credentialsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求体格式无效", nil))
		return
	}

	if fieldErrs := validateCredentials(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "输入字段校验失败", fieldErrs))
		return
	}

	result, err := h.svc.Register(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		h.logger.Info("register failed", "request_id", c.GetString("X-Request-Id"), "error_code", errorCodeOf(err))
		c.JSON(statusOf(err), newErrorEnvelope(errorCodeOf(err), err.Error(), nil))
		return
	}

	h.logger.Info("user registered", "request_id", c.GetString("X-Request-Id"), "user_id", result.User.ID)
	c.JSON(http.StatusCreated, gin.H{"data": result})
}

// Login handles POST /api/v1/auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req credentialsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求体格式无效", nil))
		return
	}

	if fieldErrs := validateCredentials(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "输入字段校验失败", fieldErrs))
		return
	}

	result, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		h.logger.Info("login failed", "request_id", c.GetString("X-Request-Id"), "error_code", errorCodeOf(err))
		c.JSON(statusOf(err), newErrorEnvelope(errorCodeOf(err), err.Error(), nil))
		return
	}

	h.logger.Info("user logged in", "request_id", c.GetString("X-Request-Id"), "user_id", result.User.ID)
	c.JSON(http.StatusOK, gin.H{"data": result})
}

// Logout handles POST /api/v1/auth/logout. Idempotent (see contracts/auth-api.md).
func (h *Handler) Logout(c *gin.Context) {
	tokenHash := c.GetString(ContextTokenHash)
	if tokenHash == "" {
		// Protocol-layer error only: missing/malformed header (middleware normally rejects).
		c.JSON(http.StatusUnauthorized, newErrorEnvelope("AUTH_INVALID_TOKEN", "缺少或无效的认证令牌", nil))
		return
	}

	if err := h.svc.Logout(c.Request.Context(), tokenHash); err != nil {
		h.logger.Error("logout failed", "request_id", c.GetString("X-Request-Id"), "error", err.Error())
		c.JSON(http.StatusInternalServerError, newErrorEnvelope("INTERNAL_ERROR", "内部错误", nil))
		return
	}

	h.logger.Info("user logged out", "request_id", c.GetString("X-Request-Id"))
	// Return a JSON body (not 204): the frontend response interceptor treats an
	// empty-body 2xx as a failure and would surface a spurious error toast.
	c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
}

// Me handles GET /api/v1/auth/me.
func (h *Handler) Me(c *gin.Context) {
	principal, ok := c.Get(ContextAuthUser)
	if !ok {
		c.JSON(http.StatusUnauthorized, newErrorEnvelope("AUTH_INVALID_TOKEN", "缺少或无效的认证令牌", nil))
		return
	}
	user := principal.(*AuthUser)

	profile, err := h.svc.GetUserProfile(c.Request.Context(), user.ID)
	if err != nil {
		h.logger.Error("get profile failed", "request_id", c.GetString("X-Request-Id"), "error", err.Error())
		c.JSON(http.StatusInternalServerError, newErrorEnvelope("INTERNAL_ERROR", "内部错误", nil))
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{"user": profile}})
}

// validateCredentials checks username/password format rules.
func validateCredentials(req credentialsRequest) []fieldError {
	var errs []fieldError

	username := strings.TrimSpace(req.Username)
	switch {
	case username == "":
		errs = append(errs, fieldError{Field: "username", Code: "REQUIRED", Message: "用户名不能为空"})
	case len(username) < minUsernameLen || len(username) > maxUsernameLen:
		errs = append(errs, fieldError{Field: "username", Code: "LENGTH", Message: "用户名长度需为 3-32 个字符"})
	case !usernameRe.MatchString(username):
		errs = append(errs, fieldError{Field: "username", Code: "FORMAT", Message: "用户名仅允许字母、数字和下划线"})
	}

	switch {
	case req.Password == "":
		errs = append(errs, fieldError{Field: "password", Code: "REQUIRED", Message: "密码不能为空"})
	case len(req.Password) < minPasswordLen:
		errs = append(errs, fieldError{Field: "password", Code: "TOO_SHORT", Message: "密码长度至少 8 个字符"})
	case len(req.Password) > maxPasswordLen:
		errs = append(errs, fieldError{Field: "password", Code: "TOO_LONG", Message: "密码长度不能超过 72 个字符"})
	}

	return errs
}

func newErrorEnvelope(code, message string, fieldErrs []fieldError) errorEnvelope {
	var e errorEnvelope
	e.Error.Code = code
	e.Error.Message = message
	e.Error.FieldErrors = fieldErrs
	return e
}

// errorCodeOf maps a service sentinel error to its contract error code.
func errorCodeOf(err error) string {
	switch {
	case errors.Is(err, ErrUsernameTaken):
		return "AUTH_USERNAME_TAKEN"
	case errors.Is(err, ErrInvalidCredentials):
		return "AUTH_INVALID_CREDENTIALS"
	case errors.Is(err, ErrInvalidToken):
		return "AUTH_INVALID_TOKEN"
	case errors.Is(err, ErrInvalidInput):
		return "AUTH_INVALID_INPUT"
	default:
		return "INTERNAL_ERROR"
	}
}

// statusOf maps a service sentinel error to its HTTP status.
func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrUsernameTaken):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrInvalidToken):
		return http.StatusUnauthorized
	case errors.Is(err, ErrInvalidInput):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
