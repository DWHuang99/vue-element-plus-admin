// Shared BFF response helpers: the uniform error envelope and the stable
// mapping of IAM/Organization domain errors and BFF application Kinds to the
// frozen public codes/statuses (wire parity, contracts/http-api-compatibility.md).
package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// deleteProtectedFieldErrors mirrors the legacy DELETE_PROTECTED body.
var deleteProtectedFieldErrors = []FieldErrorDTO{{
	Field:   "ids",
	Code:    "REFERENCES",
	Message: "请先处理关联数据",
}}

// errorMapping maps an application error to its contract code/status/message.
// BFF Kinds are checked first (they wrap whole compositions), then the IAM
// and Organization domain errors.
func errorMapping(err error) (code string, status int, message string, fieldErrs []FieldErrorDTO) {
	var bffErr *adminbff.Error
	if errors.As(err, &bffErr) {
		switch bffErr.Kind {
		case adminbff.KindInvalidInput:
			fields := make([]FieldErrorDTO, 0, len(bffErr.Fields))
			for _, f := range bffErr.Fields {
				fields = append(fields, FieldErrorDTO{Field: f.Field, Code: f.Code, Message: f.Message})
			}
			return "AUTH_INVALID_INPUT", http.StatusBadRequest, "请求参数校验失败", fields
		case adminbff.KindUserNotFound:
			return "USER_NOT_FOUND", http.StatusNotFound, "用户不存在", nil
		case adminbff.KindIdempotencyConflict:
			return "IDEMPOTENCY_CONFLICT", http.StatusConflict, "幂等键冲突，请检查请求参数", nil
		case adminbff.KindOperationInProgress:
			return "OPERATION_IN_PROGRESS", http.StatusConflict, "相同操作正在执行中", nil
		case adminbff.KindOperationExpired:
			return "OPERATION_EXPIRED", http.StatusConflict, "操作已过期，请使用新的幂等键重新提交", nil
		case adminbff.KindDependencyUnavailable:
			return "DEPENDENCY_UNAVAILABLE", http.StatusServiceUnavailable, "依赖服务不可用", nil
		case adminbff.KindDependencyTimeout:
			return "DEPENDENCY_TIMEOUT", http.StatusGatewayTimeout, "依赖服务超时", nil
		case adminbff.KindWorkflowRetryable:
			return "WORKFLOW_RETRYABLE", http.StatusServiceUnavailable, "操作可稍后重试", nil
		case adminbff.KindReconciliationRequired:
			return "RECONCILIATION_REQUIRED", http.StatusInternalServerError, "操作需要人工对账，请勿盲目重试", nil
		case adminbff.KindUnauthorized:
			return "AUTH_INVALID_TOKEN", http.StatusUnauthorized, "认证令牌无效或已过期", nil
		case adminbff.KindForbidden:
			return "AUTH_FORBIDDEN", http.StatusForbidden, "无权执行此操作", nil
		}
		return "INTERNAL_ERROR", http.StatusInternalServerError, "内部错误", nil
	}

	switch {
	case errors.Is(err, iam.ErrInvalidInput):
		return "AUTH_INVALID_INPUT", http.StatusBadRequest, "请求参数校验失败", nil
	case errors.Is(err, iam.ErrUsernameTaken):
		return "AUTH_USERNAME_TAKEN", http.StatusConflict, "用户名已存在", nil
	case errors.Is(err, iam.ErrInvalidCredentials):
		return "AUTH_INVALID_CREDENTIALS", http.StatusUnauthorized, "用户名或密码错误", nil
	case errors.Is(err, iam.ErrInvalidToken):
		return "AUTH_INVALID_TOKEN", http.StatusUnauthorized, "认证令牌无效、已过期或已撤销", nil
	case errors.Is(err, iam.ErrUserNotFound):
		return "USER_NOT_FOUND", http.StatusNotFound, "用户不存在", nil
	case errors.Is(err, iam.ErrRoleNotFound):
		return "ROLE_NOT_FOUND", http.StatusNotFound, "角色不存在", nil
	case errors.Is(err, iam.ErrNameTaken):
		return "NAME_TAKEN", http.StatusConflict, "名称已存在", nil
	case errors.Is(err, iam.ErrBuiltinRoleCodeImmutable):
		return "BUILTIN_ROLE_CODE_IMMUTABLE", http.StatusConflict, "内置角色代码不可修改", nil
	case errors.Is(err, iam.ErrBuiltinRoleDeleteProtected):
		return "BUILTIN_ROLE_DELETE_PROTECTED", http.StatusConflict, "内置角色不可删除", nil
	case errors.Is(err, iam.ErrDeleteProtected):
		return "DELETE_PROTECTED", http.StatusBadRequest,
			"存在关联数据（下级部门/用户/角色引用），无法删除", deleteProtectedFieldErrors
	case errors.Is(err, organization.ErrDepartmentNotFound):
		return "DEPARTMENT_NOT_FOUND", http.StatusNotFound, "部门不存在", nil
	case errors.Is(err, organization.ErrNameTaken):
		return "NAME_TAKEN", http.StatusConflict, "名称已存在", nil
	case errors.Is(err, organization.ErrDeleteProtected):
		return "DELETE_PROTECTED", http.StatusBadRequest,
			"存在关联数据（下级部门/用户/角色引用），无法删除", deleteProtectedFieldErrors
	default:
		return "INTERNAL_ERROR", http.StatusInternalServerError, "内部错误", nil
	}
}

// retryAfterPolicy is the exact Retry-After table from
// contracts/http-api-compatibility.md workflow and dependency mapping: only
// the four retry codes carry the header; the attached application value is
// honored when an integer in 1–60, otherwise the code's contract default.
// Every other code (including IDEMPOTENCY_CONFLICT, OPERATION_EXPIRED and
// RECONCILIATION_REQUIRED) MUST NOT carry Retry-After — presence/range is
// enforced here, never by the application layer.
func retryAfterPolicy(code string, attached int) (int, bool) {
	defaults := map[string]int{
		"OPERATION_IN_PROGRESS":  3,
		"DEPENDENCY_UNAVAILABLE": 5,
		"DEPENDENCY_TIMEOUT":     3,
		"WORKFLOW_RETRYABLE":     3,
	}
	def, carries := defaults[code]
	if !carries {
		return 0, false
	}
	if attached >= 1 && attached <= 60 {
		return attached, true
	}
	return def, true
}

// writeError emits the contract error envelope and logs the failure. The
// Retry-After header follows retryAfterPolicy; messages are the fixed public
// strings from errorMapping — dependency/receipt/step internals never leak
// and no dependency/workflow/SQL/transport error maps to 401/403.
func writeError(c *gin.Context, logger *slog.Logger, err error) {
	code, status, message, fieldErrs := errorMapping(err)
	// T074: count every rejected operation by its stable contract code. The
	// code comes from the fixed errorMapping table (upper-case identifiers,
	// metric-name safe), so no sanitization is needed here.
	if reg := middleware.MetricsFromContext(c); reg != nil {
		reg.AddCounter("bff_errors_total", 1)
		reg.AddCounter("bff_errors_"+code+"_total", 1)
	}
	requestID := c.GetString(middleware.RequestIDHeader)
	if status >= http.StatusInternalServerError {
		logger.Error("adminbff operation failed", "request_id", requestID, "error", err.Error())
	} else {
		logger.Warn("adminbff operation rejected", "request_id", requestID, "code", code, "error", err.Error())
	}

	var bffErr *adminbff.Error
	attached := 0
	if errors.As(err, &bffErr) {
		attached = bffErr.RetryAfter
	}
	if seconds, ok := retryAfterPolicy(code, attached); ok {
		c.Header("Retry-After", strconv.Itoa(seconds))
	}

	c.JSON(status, ErrorEnvelope{Error: ErrorBodyDTO{
		Code:        code,
		Message:     message,
		FieldErrors: fieldErrs,
	}})
}

// writeData emits the success envelope {"data": payload}.
func writeData(c *gin.Context, status int, payload any) {
	c.JSON(status, DataEnvelope{Data: payload})
}
