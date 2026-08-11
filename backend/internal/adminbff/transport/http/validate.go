// Validation helpers replicating the frozen legacy rules
// (contracts/http-api-compatibility.md): same fields, codes and Chinese
// messages, so the frontend sees byte-identical field errors under either
// router.
package http

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

var (
	usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	roleCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	emailRe    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

// credentialsRequest is the shared register/login body.
type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// validateCredentials checks the shared username/password rules.
func validateCredentials(req credentialsRequest) []FieldErrorDTO {
	var errs []FieldErrorDTO
	username := strings.TrimSpace(req.Username)
	switch {
	case username == "":
		errs = append(errs, FieldErrorDTO{Field: "username", Code: "REQUIRED", Message: "用户名不能为空"})
	case len(username) < 3 || len(username) > 32:
		errs = append(errs, FieldErrorDTO{Field: "username", Code: "LENGTH", Message: "用户名长度需为 3-32 个字符"})
	case !usernameRe.MatchString(username):
		errs = append(errs, FieldErrorDTO{Field: "username", Code: "FORMAT", Message: "用户名仅允许字母、数字和下划线"})
	}
	switch {
	case req.Password == "":
		errs = append(errs, FieldErrorDTO{Field: "password", Code: "REQUIRED", Message: "密码不能为空"})
	case len(req.Password) < 8:
		errs = append(errs, FieldErrorDTO{Field: "password", Code: "TOO_SHORT", Message: "密码长度至少 8 个字符"})
	case len(req.Password) > 72:
		errs = append(errs, FieldErrorDTO{Field: "password", Code: "TOO_LONG", Message: "密码长度不能超过 72 个字符"})
	}
	return errs
}

// saveRoleRequest is the POST /roles body.
type saveRoleRequest struct {
	ID   *int64 `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// validateRole checks the role name/code rules.
func validateRole(req saveRoleRequest) []FieldErrorDTO {
	var errs []FieldErrorDTO
	switch {
	case strings.TrimSpace(req.Name) == "":
		errs = append(errs, FieldErrorDTO{Field: "name", Code: "REQUIRED", Message: "角色名不能为空"})
	case len(req.Name) > 64:
		errs = append(errs, FieldErrorDTO{Field: "name", Code: "LENGTH", Message: "角色名不能超过 64 个字符"})
	}
	switch {
	case req.Code == "":
		errs = append(errs, FieldErrorDTO{Field: "code", Code: "REQUIRED", Message: "角色码不能为空"})
	case !roleCodeRe.MatchString(req.Code):
		errs = append(errs, FieldErrorDTO{Field: "code", Code: "FORMAT", Message: "角色码需以小写字母开头，仅含小写字母、数字、下划线"})
	}
	return errs
}

// saveDepartmentRequest is the POST /departments body.
type saveDepartmentRequest struct {
	ID       *int64 `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"`
}

// validateDepartment checks the department name/parent rules.
func validateDepartment(req saveDepartmentRequest) []FieldErrorDTO {
	var errs []FieldErrorDTO
	switch {
	case strings.TrimSpace(req.Name) == "":
		errs = append(errs, FieldErrorDTO{Field: "name", Code: "REQUIRED", Message: "部门名不能为空"})
	case len(req.Name) > 64:
		errs = append(errs, FieldErrorDTO{Field: "name", Code: "LENGTH", Message: "部门名不能超过 64 个字符"})
	}
	if req.ParentID != nil && *req.ParentID < 1 {
		errs = append(errs, FieldErrorDTO{Field: "parent_id", Code: "INVALID", Message: "父部门 id 无效"})
	}
	if req.ID != nil && *req.ID < 1 {
		errs = append(errs, FieldErrorDTO{Field: "id", Code: "INVALID", Message: "部门 id 无效"})
	}
	return errs
}

// saveUserRequest is the POST /users body (create when id is absent, update
// otherwise), mirroring the legacy save-user request shape byte-for-byte so
// the frontend sees identical field errors under either router
// (contracts/http-api-compatibility.md POST /users retains fields and
// validation).
type saveUserRequest struct {
	ID           *int64  `json:"id"`
	Username     string  `json:"username"`
	Account      string  `json:"account"`
	Email        string  `json:"email"`
	Password     *string `json:"password"`
	DepartmentID *int64  `json:"department_id"`
	Roles        []int64 `json:"roles"`
}

// validateUser checks the create/update user rules with the frozen legacy
// codes/messages (rbac/handler.go validateUser).
func validateUser(req saveUserRequest) []FieldErrorDTO {
	var errs []FieldErrorDTO
	username := strings.TrimSpace(req.Username)
	switch {
	case username == "":
		errs = append(errs, FieldErrorDTO{Field: "username", Code: "REQUIRED", Message: "用户名不能为空"})
	case len(username) < 3 || len(username) > 32:
		errs = append(errs, FieldErrorDTO{Field: "username", Code: "LENGTH", Message: "用户名长度需为 3-32 个字符"})
	case !usernameRe.MatchString(username):
		errs = append(errs, FieldErrorDTO{Field: "username", Code: "FORMAT", Message: "用户名仅允许字母、数字和下划线"})
	}
	if len(req.Account) > 64 {
		errs = append(errs, FieldErrorDTO{Field: "account", Code: "LENGTH", Message: "账号不能超过 64 个字符"})
	}
	if req.Email != "" && !emailRe.MatchString(req.Email) {
		errs = append(errs, FieldErrorDTO{Field: "email", Code: "FORMAT", Message: "邮箱格式不正确"})
	}
	// Create (no id) requires a password; updates may omit it.
	if req.ID == nil && (req.Password == nil || *req.Password == "") {
		errs = append(errs, FieldErrorDTO{Field: "password", Code: "REQUIRED", Message: "新建用户必须设置密码"})
	}
	if req.Password != nil {
		switch {
		case len(*req.Password) < 8:
			errs = append(errs, FieldErrorDTO{Field: "password", Code: "TOO_SHORT", Message: "密码长度至少 8 个字符"})
		case len(*req.Password) > 72:
			errs = append(errs, FieldErrorDTO{Field: "password", Code: "TOO_LONG", Message: "密码长度不能超过 72 个字符"})
		}
	}
	if req.DepartmentID != nil && *req.DepartmentID < 1 {
		errs = append(errs, FieldErrorDTO{Field: "department_id", Code: "INVALID", Message: "部门 id 无效"})
	}
	if req.ID != nil && *req.ID < 1 {
		errs = append(errs, FieldErrorDTO{Field: "id", Code: "INVALID", Message: "用户 id 无效"})
	}
	for i, roleID := range req.Roles {
		if roleID < 1 {
			errs = append(errs, FieldErrorDTO{Field: "roles", Code: "INVALID", Message: "第 " + strconv.Itoa(i+1) + " 个角色 id 无效"})
		}
	}
	return errs
}

// deleteRequest is the shared batch-delete body.
type deleteRequest struct {
	IDs []int64 `json:"ids"`
}

// validateIDs checks the batch-delete id rules.
func validateIDs(ids []int64) []FieldErrorDTO {
	if len(ids) == 0 {
		return []FieldErrorDTO{{Field: "ids", Code: "REQUIRED", Message: "ids 不能为空"}}
	}
	for i, id := range ids {
		if id < 1 {
			return []FieldErrorDTO{{Field: "ids", Code: "INVALID", Message: "第 " + strconv.Itoa(i+1) + " 个 id 无效"}}
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

// queryInt64 parses a 64-bit integer query param with a fallback default.
func queryInt64(c *gin.Context, key string, def int64) int64 {
	if v := c.Query(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
