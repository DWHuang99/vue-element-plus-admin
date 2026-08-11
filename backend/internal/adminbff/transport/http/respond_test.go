// T057 public error mapping and Retry-After contract tests. The full
// presence/range/default table from contracts/http-api-compatibility.md
// "Workflow and dependency failure mapping" is asserted at the transport
// level: exact code/status, Retry-After only for the four retry codes with
// 1–60 enforcement and per-code defaults, never 401/403 for dependency/
// workflow/SQL/transport errors, and fixed public messages (no receipt/
// step/compensation internals).
package http

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// emit writes err through writeError on a fresh recorder and returns the
// status, code, Retry-After header and public message.
func emit(t *testing.T, err error) (int, string, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writeError(c, logger, err)

	var decoded struct {
		Error ErrorBodyDTO `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	return rec.Code, decoded.Error.Code, rec.Header().Get("Retry-After"), decoded.Error.Message
}

func TestErrorMapping_RetryAfterMatrix(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  string
		wantHTTP  int
		wantRetry string // "" = header MUST NOT be present
	}{
		// Retry codes with contract defaults when unattached.
		{"operation in progress default 3", adminbff.ErrOperationInProgress, "OPERATION_IN_PROGRESS", http.StatusConflict, "3"},
		{"dependency unavailable default 5", adminbff.ErrDependencyUnavailable, "DEPENDENCY_UNAVAILABLE", http.StatusServiceUnavailable, "5"},
		{"dependency timeout default 3", adminbff.ErrDependencyTimeout, "DEPENDENCY_TIMEOUT", http.StatusGatewayTimeout, "3"},
		{"workflow retryable default 3", adminbff.ErrWorkflowRetryable, "WORKFLOW_RETRYABLE", http.StatusServiceUnavailable, "3"},

		// Attached values honored only inside 1–60; out-of-range falls back to default.
		{"attached in range kept", adminbff.ErrWorkflowRetryable.WithRetryAfter(5), "WORKFLOW_RETRYABLE", http.StatusServiceUnavailable, "5"},
		{"attached 60 kept", adminbff.ErrDependencyUnavailable.WithRetryAfter(60), "DEPENDENCY_UNAVAILABLE", http.StatusServiceUnavailable, "60"},
		{"attached 0 falls back", adminbff.ErrDependencyUnavailable.WithRetryAfter(0), "DEPENDENCY_UNAVAILABLE", http.StatusServiceUnavailable, "5"},
		{"attached 61 falls back", adminbff.ErrOperationInProgress.WithRetryAfter(61), "OPERATION_IN_PROGRESS", http.StatusConflict, "3"},
		{"attached negative falls back", adminbff.ErrDependencyTimeout.WithRetryAfter(-1), "DEPENDENCY_TIMEOUT", http.StatusGatewayTimeout, "3"},

		// Codes that MUST NOT carry Retry-After even when the application
		// error attached one (transport enforces absence).
		{"idempotency conflict no retry-after", adminbff.ErrIdempotencyConflict.WithRetryAfter(3), "IDEMPOTENCY_CONFLICT", http.StatusConflict, ""},
		{"operation expired no retry-after", adminbff.ErrOperationExpired.WithRetryAfter(3), "OPERATION_EXPIRED", http.StatusConflict, ""},
		{"reconciliation required no retry-after", adminbff.ErrReconciliationRequired.WithRetryAfter(3), "RECONCILIATION_REQUIRED", http.StatusInternalServerError, ""},

		// Other public codes never carry the header.
		{"user not found", adminbff.ErrUserNotFound, "USER_NOT_FOUND", http.StatusNotFound, ""},
		{"forbidden", adminbff.ErrForbidden, "AUTH_FORBIDDEN", http.StatusForbidden, ""},
		{"unauthorized", adminbff.ErrUnauthorized, "AUTH_INVALID_TOKEN", http.StatusUnauthorized, ""},
		{"invalid input", adminbff.ErrInvalidInput, "AUTH_INVALID_INPUT", http.StatusBadRequest, ""},

		// IAM/Organization domain errors (legacy parity codes).
		{"iam username taken", iam.ErrUsernameTaken, "AUTH_USERNAME_TAKEN", http.StatusConflict, ""},
		{"iam user not found", iam.ErrUserNotFound, "USER_NOT_FOUND", http.StatusNotFound, ""},
		{"iam invalid credentials", iam.ErrInvalidCredentials, "AUTH_INVALID_CREDENTIALS", http.StatusUnauthorized, ""},
		{"org delete protected", organization.ErrDeleteProtected, "DELETE_PROTECTED", http.StatusBadRequest, ""},

		// Wrapped errors resolve the same way (errors.As through %w chains).
		{"wrapped workflow retryable", errors.Join(adminbff.ErrWorkflowRetryable.WithRetryAfter(7), errors.New("leaf")), "WORKFLOW_RETRYABLE", http.StatusServiceUnavailable, "7"},
		{"wrapped dependency timeout", errors.Join(adminbff.ErrDependencyTimeout, errors.New("leaf")), "DEPENDENCY_TIMEOUT", http.StatusGatewayTimeout, "3"},

		// Raw SQL/transport failures are internal — never 401/403.
		{"raw sql error", errors.New("sql: connection refused"), "INTERNAL_ERROR", http.StatusInternalServerError, ""},
		{"raw transport error", errors.New("net/http: TLS handshake timeout"), "INTERNAL_ERROR", http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, retry, message := emit(t, tc.err)
			assert.Equal(t, tc.wantHTTP, status, "http status")
			assert.Equal(t, tc.wantCode, code, "public code")
			assert.Equal(t, tc.wantRetry, retry, "Retry-After presence/value")
			// Internal details (receipt ids, step names, sql text) never leak
			// into the public message: it is the fixed mapping string.
			assert.NotContains(t, message, tc.err.Error(), "public message must not carry internal error text")
			assert.NotEmpty(t, message)
		})
	}
}

func TestErrorMapping_FieldErrorsAndEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writeError(c, slog.New(slog.NewTextHandler(io.Discard, nil)),
		adminbff.NewInvalidInput("请求参数校验失败", []adminbff.FieldError{
			{Field: "username", Code: "LENGTH", Message: "用户名长度需为 3-32 位"},
			{Field: "password", Code: "TOO_SHORT", Message: "密码长度不足"},
		}))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.Bytes()

	// Envelope field names/types are frozen (contracts/http-api-compatibility.md).
	decoded := map[string]any{}
	require.NoError(t, json.Unmarshal(body, &decoded))
	env, ok := decoded["error"].(map[string]any)
	require.True(t, ok, "envelope has error object")
	assert.Equal(t, "AUTH_INVALID_INPUT", env["code"])
	fields, ok := env["field_errors"].([]any)
	require.True(t, ok)
	require.Len(t, fields, 2)
	first := fields[0].(map[string]any)
	assert.Equal(t, "username", first["field"])
	assert.Equal(t, "LENGTH", first["code"])
	assert.Empty(t, env["retry_after"], "no retry_after key in envelope")
}
