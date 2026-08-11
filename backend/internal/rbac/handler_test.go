//go:build rollback

package rbac

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// mockService is a test double for the Service interface (protocol-layer tests only).
type mockService struct {
	listRolesFn         func(ctx context.Context) ([]RoleView, error)
	saveRoleFn          func(ctx context.Context, p SaveRoleParams) error
	deleteRolesFn       func(ctx context.Context, ids []int64) error
	listDepartmentsFn   func(ctx context.Context) ([]DepartmentView, error)
	saveDepartmentFn    func(ctx context.Context, p SaveDepartmentParams) error
	deleteDepartmentsFn func(ctx context.Context, ids []int64) error
	listUsersFn         func(ctx context.Context, p ListUsersParams) (*UserListResult, error)
	saveUserFn          func(ctx context.Context, p SaveUserParams) error
	deleteUsersFn       func(ctx context.Context, ids []int64) error
}

func (m *mockService) ListRoles(ctx context.Context) ([]RoleView, error) {
	if m.listRolesFn != nil {
		return m.listRolesFn(ctx)
	}
	return nil, nil
}
func (m *mockService) SaveRole(ctx context.Context, p SaveRoleParams) error {
	if m.saveRoleFn != nil {
		return m.saveRoleFn(ctx, p)
	}
	return nil
}
func (m *mockService) DeleteRoles(ctx context.Context, ids []int64) error {
	if m.deleteRolesFn != nil {
		return m.deleteRolesFn(ctx, ids)
	}
	return nil
}
func (m *mockService) ListDepartments(ctx context.Context) ([]DepartmentView, error) {
	if m.listDepartmentsFn != nil {
		return m.listDepartmentsFn(ctx)
	}
	return nil, nil
}
func (m *mockService) SaveDepartment(ctx context.Context, p SaveDepartmentParams) error {
	if m.saveDepartmentFn != nil {
		return m.saveDepartmentFn(ctx, p)
	}
	return nil
}
func (m *mockService) DeleteDepartments(ctx context.Context, ids []int64) error {
	if m.deleteDepartmentsFn != nil {
		return m.deleteDepartmentsFn(ctx, ids)
	}
	return nil
}
func (m *mockService) ListUsers(ctx context.Context, p ListUsersParams) (*UserListResult, error) {
	if m.listUsersFn != nil {
		return m.listUsersFn(ctx, p)
	}
	return &UserListResult{List: []UserListView{}, Total: 0}, nil
}
func (m *mockService) SaveUser(ctx context.Context, p SaveUserParams) error {
	if m.saveUserFn != nil {
		return m.saveUserFn(ctx, p)
	}
	return nil
}
func (m *mockService) DeleteUsers(ctx context.Context, ids []int64) error {
	if m.deleteUsersFn != nil {
		return m.deleteUsersFn(ctx, ids)
	}
	return nil
}

func newTestHandler(m Service) *Handler {
	return NewHandler(m, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func setupRouter(h *Handler) *gin.Engine {
	r := gin.New()
	r.GET("/roles", h.ListRoles)
	r.POST("/roles", h.SaveRole)
	r.POST("/roles/delete", h.DeleteRoles)
	r.GET("/departments", h.ListDepartments)
	r.POST("/departments", h.SaveDepartment)
	r.POST("/departments/delete", h.DeleteDepartments)
	r.GET("/users", h.ListUsers)
	r.POST("/users", h.SaveUser)
	r.POST("/users/delete", h.DeleteUsers)
	return r
}

func doReq(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) (string, []struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}) {
	t.Helper()
	var env struct {
		Error struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			FieldErrors []struct {
				Field   string `json:"field"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"field_errors"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	return env.Error.Code, env.Error.FieldErrors
}

// --- roles contract ---

func TestListRoles_Handler(t *testing.T) {
	m := &mockService{listRolesFn: func(ctx context.Context) ([]RoleView, error) {
		return []RoleView{{ID: 1, Name: "超级管理员", Code: "super_admin", IsBuiltin: true}}, nil
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodGet, "/roles", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var res struct {
		Data struct {
			List  []RoleView `json:"list"`
			Total int        `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Len(t, res.Data.List, 1)
	assert.Equal(t, 1, res.Data.Total)
	assert.True(t, res.Data.List[0].IsBuiltin)
}

func TestSaveRole_Handler_Valid(t *testing.T) {
	m := &mockService{}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles", map[string]any{"name": "运营", "code": "operator"})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"data":{}`)
}

func TestSaveRole_Handler_InvalidCode(t *testing.T) {
	m := &mockService{}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles", map[string]any{"name": "运营", "code": "Bad Code"})
	require.Equal(t, http.StatusBadRequest, w.Code)
	code, fieldErrs := decodeErr(t, w)
	assert.Equal(t, "AUTH_INVALID_INPUT", code)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "code", fieldErrs[0].Field)
}

func TestSaveRole_Handler_NameTaken(t *testing.T) {
	m := &mockService{saveRoleFn: func(ctx context.Context, p SaveRoleParams) error { return ErrNameTaken }}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles", map[string]any{"name": "普通用户", "code": "dup"})
	require.Equal(t, http.StatusConflict, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "NAME_TAKEN", code)
}

func TestSaveRole_Handler_BuiltinCodeImmutable(t *testing.T) {
	m := &mockService{saveRoleFn: func(ctx context.Context, p SaveRoleParams) error {
		return ErrBuiltinRoleCodeImmutable
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles", map[string]any{"id": 1, "name": "管理员", "code": "renamed_admin"})
	require.Equal(t, http.StatusConflict, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "BUILTIN_ROLE_CODE_IMMUTABLE", code)
}

func TestDeleteRoles_Handler_EmptyIDs(t *testing.T) {
	m := &mockService{}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles/delete", map[string]any{"ids": []int64{}})
	require.Equal(t, http.StatusBadRequest, w.Code)
	code, fieldErrs := decodeErr(t, w)
	assert.Equal(t, "AUTH_INVALID_INPUT", code)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "ids", fieldErrs[0].Field)
}

func TestDeleteRoles_Handler_Protected(t *testing.T) {
	m := &mockService{deleteRolesFn: func(ctx context.Context, ids []int64) error { return ErrDeleteProtected }}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles/delete", map[string]any{"ids": []int64{1}})
	require.Equal(t, http.StatusBadRequest, w.Code)
	code, fieldErrs := decodeErr(t, w)
	assert.Equal(t, "DELETE_PROTECTED", code)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "ids", fieldErrs[0].Field)
}

func TestDeleteRoles_Handler_BuiltinProtected(t *testing.T) {
	m := &mockService{deleteRolesFn: func(ctx context.Context, ids []int64) error {
		return ErrBuiltinRoleDeleteProtected
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles/delete", map[string]any{"ids": []int64{1}})
	require.Equal(t, http.StatusConflict, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "BUILTIN_ROLE_DELETE_PROTECTED", code)
}

func TestDeleteRoles_Handler_NotFound(t *testing.T) {
	m := &mockService{deleteRolesFn: func(ctx context.Context, ids []int64) error { return ErrRoleNotFound }}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/roles/delete", map[string]any{"ids": []int64{99}})
	require.Equal(t, http.StatusNotFound, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "ROLE_NOT_FOUND", code)
}

// --- departments contract ---

func TestListDepartments_Handler_Tree(t *testing.T) {
	m := &mockService{listDepartmentsFn: func(ctx context.Context) ([]DepartmentView, error) {
		child := DepartmentView{ID: 2, Name: "前端组"}
		return []DepartmentView{{ID: 1, Name: "研发部", Children: []DepartmentView{child}}}, nil
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodGet, "/departments", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var res struct {
		Data struct {
			List []DepartmentView `json:"list"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	require.Len(t, res.Data.List, 1)
	assert.Len(t, res.Data.List[0].Children, 1)
}

func TestSaveDepartment_Handler_SelfParent(t *testing.T) {
	// Self-parent rejection lives in the service; the handler maps it to 400.
	m := &mockService{saveDepartmentFn: func(ctx context.Context, p SaveDepartmentParams) error {
		return ErrInvalidInput
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/departments",
		map[string]any{"id": 1, "name": "研发部", "parent_id": 1})
	require.Equal(t, http.StatusBadRequest, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "AUTH_INVALID_INPUT", code)
}

func TestSaveDepartment_Handler_NotFound(t *testing.T) {
	m := &mockService{saveDepartmentFn: func(ctx context.Context, p SaveDepartmentParams) error {
		return ErrDepartmentNotFound
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/departments", map[string]any{"id": 99, "name": "x"})
	require.Equal(t, http.StatusNotFound, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "DEPARTMENT_NOT_FOUND", code)
}

// --- users contract ---

func TestListUsers_Handler_Pagination(t *testing.T) {
	m := &mockService{listUsersFn: func(ctx context.Context, p ListUsersParams) (*UserListResult, error) {
		assert.Equal(t, int64(3), p.DepartmentID)
		assert.Equal(t, 2, p.PageIndex)
		assert.Equal(t, 10, p.PageSize)
		return &UserListResult{
			List:  []UserListView{{ID: 7, Username: "zhang", Role: "普通用户"}},
			Total: 1,
		}, nil
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodGet, "/users?department_id=3&page_index=2&page_size=10", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var res struct {
		Data struct {
			List  []UserListView `json:"list"`
			Total int64          `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Len(t, res.Data.List, 1)
	assert.Equal(t, int64(1), res.Data.Total)
}

func TestSaveUser_Handler_MissingPasswordOnCreate(t *testing.T) {
	m := &mockService{}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/users",
		map[string]any{"username": "zhangsan", "roles": []int64{3}})
	require.Equal(t, http.StatusBadRequest, w.Code)
	code, fieldErrs := decodeErr(t, w)
	assert.Equal(t, "AUTH_INVALID_INPUT", code)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "password", fieldErrs[0].Field)
}

func TestSaveUser_Handler_InvalidEmail(t *testing.T) {
	m := &mockService{}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/users",
		map[string]any{"username": "zhangsan", "email": "not-an-email", "password": "pass-12345678"})
	require.Equal(t, http.StatusBadRequest, w.Code)
	code, fieldErrs := decodeErr(t, w)
	assert.Equal(t, "AUTH_INVALID_INPUT", code)
	require.NotEmpty(t, fieldErrs)
	assert.Equal(t, "email", fieldErrs[0].Field)
}

func TestSaveUser_Handler_UsernameTaken(t *testing.T) {
	m := &mockService{saveUserFn: func(ctx context.Context, p SaveUserParams) error { return ErrNameTaken }}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/users",
		map[string]any{"username": "zhangsan", "password": "pass-12345678", "roles": []int64{3}})
	require.Equal(t, http.StatusConflict, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "NAME_TAKEN", code)
}

func TestSaveUser_Handler_BadRole(t *testing.T) {
	m := &mockService{saveUserFn: func(ctx context.Context, p SaveUserParams) error { return ErrRoleNotFound }}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/users",
		map[string]any{"username": "zhangsan", "password": "pass-12345678", "roles": []int64{999}})
	require.Equal(t, http.StatusNotFound, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "ROLE_NOT_FOUND", code)
}

func TestDeleteUsers_Handler_NotFound(t *testing.T) {
	m := &mockService{deleteUsersFn: func(ctx context.Context, ids []int64) error { return ErrUserNotFound }}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/users/delete", map[string]any{"ids": []int64{99}})
	require.Equal(t, http.StatusNotFound, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "USER_NOT_FOUND", code)
}

// --- T076 legacy delete delegation ---

// mockDeleteDelegate records the delegation call for handler tests.
type mockDeleteDelegate struct {
	ids         []int64
	actorID     int64
	correlation string
	err         error
}

func (m *mockDeleteDelegate) DeleteUsers(ctx context.Context, actorID int64, correlationID string, ids []int64) error {
	m.actorID = actorID
	m.correlation = correlationID
	m.ids = ids
	return m.err
}

func TestDeleteUsers_Handler_DelegatesToIAM(t *testing.T) {
	// With the US5 delegation installed the route must call the delegate (not
	// the legacy service), passing the acting principal and correlation ID
	// from the auth middleware context.
	m := &mockService{deleteUsersFn: func(ctx context.Context, ids []int64) error {
		t.Error("legacy service must not be called when the delegate is installed")
		return nil
	}}
	del := &mockDeleteDelegate{}
	h := newTestHandler(m)
	h.SetUsersDeleteDelegate(del)

	req := httptest.NewRequest(http.MethodPost, "/users/delete", bytes.NewBufferString(`{"ids":[7,9]}`))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	c.Set(auth.ContextAuthUser, &auth.AuthUser{ID: 42, Username: "operator"})
	// The request-ID middleware stores the validated correlation ID in the
	// gin context (never re-read from the raw header).
	c.Set(middleware.RequestIDHeader, "corr-abc")
	h.DeleteUsers(c)

	require.Equal(t, http.StatusOK, c.Writer.Status())
	require.Equal(t, []int64{7, 9}, del.ids)
	require.Equal(t, int64(42), del.actorID, "acting principal flows into the IAM operation audit")
	require.Equal(t, "corr-abc", del.correlation, "correlation ID flows into the IAM operation audit")
}

func TestDeleteUsers_Handler_DelegateError(t *testing.T) {
	del := &mockDeleteDelegate{err: ErrUserNotFound}
	h := newTestHandler(&mockService{})
	h.SetUsersDeleteDelegate(del)
	r := setupRouter(h)

	w := doReq(t, r, http.MethodPost, "/users/delete", map[string]any{"ids": []int64{99}})
	require.Equal(t, http.StatusNotFound, w.Code)
	code, _ := decodeErr(t, w)
	assert.Equal(t, "USER_NOT_FOUND", code, "delegated errors map through the same contract envelope")
}

func TestDeleteUsers_Handler_NoDelegateKeepsLegacyPath(t *testing.T) {
	var called bool
	m := &mockService{deleteUsersFn: func(ctx context.Context, ids []int64) error {
		called = true
		return nil
	}}
	r := setupRouter(newTestHandler(m))
	w := doReq(t, r, http.MethodPost, "/users/delete", map[string]any{"ids": []int64{7}})
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, called, "without a delegate the legacy direct delete runs")
}
