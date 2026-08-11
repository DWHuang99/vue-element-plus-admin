//go:build rollback

// Handler adapts the rbac Service to HTTP (protocol layer only).
package rbac

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
)

// Validation rules (shared with the handler layer; service holds business rules).
var (
	usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	roleCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	emailRe    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// Handler exposes rbac operations over HTTP.
type Handler struct {
	svc    Service
	logger *slog.Logger
	// deleteDelegate (T076) is the US5 delegation target for POST
	// /users/delete: when set, the route calls the IAM DeleteUsers port (with
	// its outbox event + receipt) instead of the legacy direct delete. nil
	// keeps the pre-split behavior. The composition root installs it behind
	// LEGACY_DELETE_IAM_DELEGATION_ENABLED.
	deleteDelegate UsersDeleteDelegate
}

// NewHandler creates an rbac HTTP handler.
func NewHandler(svc Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// SetUsersDeleteDelegate installs the US5 delete delegation target (T076).
// Nil reverts the route to the legacy direct delete.
func (h *Handler) SetUsersDeleteDelegate(d UsersDeleteDelegate) {
	h.deleteDelegate = d
}

// --- request/response types ---

type saveRoleRequest struct {
	ID   *int64 `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

type saveDepartmentRequest struct {
	ID       *int64 `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"`
}

type deleteRequest struct {
	IDs []int64 `json:"ids"`
}

type saveUserRequest struct {
	ID           *int64  `json:"id"`
	Username     string  `json:"username"`
	Account      string  `json:"account"`
	Email        string  `json:"email"`
	Password     *string `json:"password"`
	DepartmentID *int64  `json:"department_id"`
	Roles        []int64 `json:"roles"`
}

// fieldError describes a single validation failure.
type fieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorBody struct {
	Code        string       `json:"code"`
	Message     string       `json:"message"`
	FieldErrors []fieldError `json:"field_errors,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

func newErrorEnvelope(code, message string, fieldErrs []fieldError) errorEnvelope {
	return errorEnvelope{Error: errorBody{Code: code, Message: message, FieldErrors: fieldErrs}}
}

// errorMapping maps a service sentinel error to its contract code/status/message.
func errorMapping(err error) (code string, status int, message string, fieldErrs []fieldError) {
	switch {
	case errors.Is(err, ErrRoleNotFound):
		return "ROLE_NOT_FOUND", http.StatusNotFound, "角色不存在", nil
	case errors.Is(err, ErrDepartmentNotFound):
		return "DEPARTMENT_NOT_FOUND", http.StatusNotFound, "部门不存在", nil
	case errors.Is(err, ErrUserNotFound):
		return "USER_NOT_FOUND", http.StatusNotFound, "用户不存在", nil
	case errors.Is(err, ErrNameTaken):
		return "NAME_TAKEN", http.StatusConflict, "名称已存在", nil
	case errors.Is(err, ErrBuiltinRoleCodeImmutable):
		return "BUILTIN_ROLE_CODE_IMMUTABLE", http.StatusConflict, "内置角色代码不可修改", nil
	case errors.Is(err, ErrBuiltinRoleDeleteProtected):
		return "BUILTIN_ROLE_DELETE_PROTECTED", http.StatusConflict, "内置角色不可删除", nil
	case errors.Is(err, ErrDeleteProtected):
		return "DELETE_PROTECTED", http.StatusBadRequest,
			"存在关联数据（下级部门/用户/角色引用），无法删除", []fieldError{{Field: "ids", Code: "REFERENCES", Message: "请先处理关联数据"}}
	case errors.Is(err, ErrInvalidInput):
		return "AUTH_INVALID_INPUT", http.StatusBadRequest, "请求参数校验失败", nil
	default:
		return "INTERNAL_ERROR", http.StatusInternalServerError, "内部错误", nil
	}
}

// fail writes the contract error envelope and logs the failure.
func (h *Handler) fail(c *gin.Context, err error) {
	code, status, message, fieldErrs := errorMapping(err)
	if status == http.StatusInternalServerError {
		h.logger.Error("rbac operation failed", "request_id", c.GetString("X-Request-Id"), "error", err.Error())
	} else {
		h.logger.Warn("rbac operation rejected", "request_id", c.GetString("X-Request-Id"), "code", code, "error", err.Error())
	}
	c.JSON(status, newErrorEnvelope(code, message, fieldErrs))
}

// --- roles ---

// ListRoles handles GET /api/v1/roles.
func (h *Handler) ListRoles(c *gin.Context) {
	list, err := h.svc.ListRoles(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"list": list, "total": len(list)}})
}

// SaveRole handles POST /api/v1/roles.
func (h *Handler) SaveRole(c *gin.Context) {
	var req saveRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, ErrInvalidInput)
		return
	}
	if fieldErrs := validateRole(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求参数校验失败", fieldErrs))
		return
	}
	if err := h.svc.SaveRole(c.Request.Context(), SaveRoleParams{
		ID:   req.ID,
		Name: req.Name,
		Code: req.Code,
	}); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
}

// DeleteRoles handles POST /api/v1/roles/delete.
func (h *Handler) DeleteRoles(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, ErrInvalidInput)
		return
	}
	if fieldErrs := validateIDs(req.IDs); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求参数校验失败", fieldErrs))
		return
	}
	if err := h.svc.DeleteRoles(c.Request.Context(), req.IDs); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
}

// --- departments ---

// ListDepartments handles GET /api/v1/departments.
func (h *Handler) ListDepartments(c *gin.Context) {
	tree, err := h.svc.ListDepartments(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"list": tree}})
}

// SaveDepartment handles POST /api/v1/departments.
func (h *Handler) SaveDepartment(c *gin.Context) {
	var req saveDepartmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, ErrInvalidInput)
		return
	}
	if fieldErrs := validateDepartment(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求参数校验失败", fieldErrs))
		return
	}
	if err := h.svc.SaveDepartment(c.Request.Context(), SaveDepartmentParams{
		ID:       req.ID,
		Name:     req.Name,
		ParentID: req.ParentID,
	}); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
}

// DeleteDepartments handles POST /api/v1/departments/delete.
func (h *Handler) DeleteDepartments(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, ErrInvalidInput)
		return
	}
	if fieldErrs := validateIDs(req.IDs); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求参数校验失败", fieldErrs))
		return
	}
	if err := h.svc.DeleteDepartments(c.Request.Context(), req.IDs); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
}

// --- users ---

// ListUsers handles GET /api/v1/users.
func (h *Handler) ListUsers(c *gin.Context) {
	params := ListUsersParams{
		DepartmentID: queryInt64(c, "department_id", 0),
		Username:     c.Query("username"),
		Account:      c.Query("account"),
		PageIndex:    queryInt(c, "page_index", 1),
		PageSize:     queryInt(c, "page_size", 10),
	}
	result, err := h.svc.ListUsers(c.Request.Context(), params)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

// SaveUser handles POST /api/v1/users.
func (h *Handler) SaveUser(c *gin.Context) {
	var req saveUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, ErrInvalidInput)
		return
	}
	if fieldErrs := validateUser(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求参数校验失败", fieldErrs))
		return
	}
	if err := h.svc.SaveUser(c.Request.Context(), SaveUserParams{
		ID:           req.ID,
		Username:     req.Username,
		Account:      req.Account,
		Email:        req.Email,
		Password:     req.Password,
		DepartmentID: req.DepartmentID,
		Roles:        req.Roles,
	}); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
}

// DeleteUsers handles POST /api/v1/users/delete. With the US5 delete
// delegation installed (T076) the route runs through the IAM DeleteUsers
// port — the acting principal and correlation ID from the legacy auth
// middleware context become the operation's audit fields. Without it the
// route keeps the legacy direct delete.
func (h *Handler) DeleteUsers(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, ErrInvalidInput)
		return
	}
	if fieldErrs := validateIDs(req.IDs); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, newErrorEnvelope("AUTH_INVALID_INPUT", "请求参数校验失败", fieldErrs))
		return
	}
	if h.deleteDelegate != nil {
		actorID := int64(0)
		if principal, ok := c.Get(auth.ContextAuthUser); ok {
			if u, ok := principal.(*auth.AuthUser); ok {
				actorID = u.ID
			}
		}
		if err := h.deleteDelegate.DeleteUsers(
			c.Request.Context(), actorID, c.GetString(middleware.RequestIDHeader), req.IDs); err != nil {
			h.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
		return
	}
	if err := h.svc.DeleteUsers(c.Request.Context(), req.IDs); err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{}})
}

// --- validation helpers ---

func validateRole(req saveRoleRequest) []fieldError {
	var errs []fieldError
	switch {
	case strings.TrimSpace(req.Name) == "":
		errs = append(errs, fieldError{Field: "name", Code: "REQUIRED", Message: "角色名不能为空"})
	case len(req.Name) > 64:
		errs = append(errs, fieldError{Field: "name", Code: "LENGTH", Message: "角色名不能超过 64 个字符"})
	}
	switch {
	case req.Code == "":
		errs = append(errs, fieldError{Field: "code", Code: "REQUIRED", Message: "角色码不能为空"})
	case !roleCodeRe.MatchString(req.Code):
		errs = append(errs, fieldError{Field: "code", Code: "FORMAT", Message: "角色码需以小写字母开头，仅含小写字母、数字、下划线"})
	}
	return errs
}

func validateDepartment(req saveDepartmentRequest) []fieldError {
	var errs []fieldError
	switch {
	case strings.TrimSpace(req.Name) == "":
		errs = append(errs, fieldError{Field: "name", Code: "REQUIRED", Message: "部门名不能为空"})
	case len(req.Name) > 64:
		errs = append(errs, fieldError{Field: "name", Code: "LENGTH", Message: "部门名不能超过 64 个字符"})
	}
	if req.ParentID != nil && *req.ParentID < 1 {
		errs = append(errs, fieldError{Field: "parent_id", Code: "INVALID", Message: "父部门 id 无效"})
	}
	if req.ID != nil && *req.ID < 1 {
		errs = append(errs, fieldError{Field: "id", Code: "INVALID", Message: "部门 id 无效"})
	}
	return errs
}

func validateUser(req saveUserRequest) []fieldError {
	var errs []fieldError
	switch {
	case strings.TrimSpace(req.Username) == "":
		errs = append(errs, fieldError{Field: "username", Code: "REQUIRED", Message: "用户名不能为空"})
	case len(req.Username) < 3 || len(req.Username) > 32:
		errs = append(errs, fieldError{Field: "username", Code: "LENGTH", Message: "用户名长度需为 3-32 个字符"})
	case !usernameRe.MatchString(req.Username):
		errs = append(errs, fieldError{Field: "username", Code: "FORMAT", Message: "用户名仅允许字母、数字和下划线"})
	}
	if len(req.Account) > 64 {
		errs = append(errs, fieldError{Field: "account", Code: "LENGTH", Message: "账号不能超过 64 个字符"})
	}
	if req.Email != "" && !emailRe.MatchString(req.Email) {
		errs = append(errs, fieldError{Field: "email", Code: "FORMAT", Message: "邮箱格式不正确"})
	}
	// Create (no id) requires a password; updates may omit it.
	if req.ID == nil && (req.Password == nil || *req.Password == "") {
		errs = append(errs, fieldError{Field: "password", Code: "REQUIRED", Message: "新建用户必须设置密码"})
	}
	if req.Password != nil {
		switch {
		case len(*req.Password) < 8:
			errs = append(errs, fieldError{Field: "password", Code: "TOO_SHORT", Message: "密码长度至少 8 个字符"})
		case len(*req.Password) > 72:
			errs = append(errs, fieldError{Field: "password", Code: "TOO_LONG", Message: "密码长度不能超过 72 个字符"})
		}
	}
	if req.DepartmentID != nil && *req.DepartmentID < 1 {
		errs = append(errs, fieldError{Field: "department_id", Code: "INVALID", Message: "部门 id 无效"})
	}
	for i, roleID := range req.Roles {
		if roleID < 1 {
			errs = append(errs, fieldError{Field: "roles", Code: "INVALID", Message: "第 " + strconv.Itoa(i+1) + " 个角色 id 无效"})
		}
	}
	return errs
}

func validateIDs(ids []int64) []fieldError {
	if len(ids) == 0 {
		return []fieldError{{Field: "ids", Code: "REQUIRED", Message: "ids 不能为空"}}
	}
	for i, id := range ids {
		if id < 1 {
			return []fieldError{{Field: "ids", Code: "INVALID", Message: "第 " + strconv.Itoa(i+1) + " 个 id 无效"}}
		}
	}
	return nil
}

// queryInt parses an integer query param with a fallback default.
func queryInt(c *gin.Context, key string, def int) int {
	if v := c.Query(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// queryInt64 parses an int64 query param with a fallback default.
func queryInt64(c *gin.Context, key string, def int64) int64 {
	if v := c.Query(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
