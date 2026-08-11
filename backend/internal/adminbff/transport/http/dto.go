// Public HTTP DTOs (contracts/http-api-compatibility.md). Field names and
// envelope shapes are frozen for the Vue frontend; the transport layer maps
// application types to these and never leaks adapter types.
package http

import "time"

// --- envelopes ---

// DataEnvelope wraps every success response; writes use EmptyData so the
// body stays {"data":{}} — never an empty 204.
type DataEnvelope struct {
	Data any `json:"data"`
}

// EmptyData is the write-success payload: {"data":{}}.
type EmptyData struct{}

// FieldErrorDTO is one validation failure inside the error envelope.
type FieldErrorDTO struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorBodyDTO is the error payload.
type ErrorBodyDTO struct {
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	FieldErrors []FieldErrorDTO `json:"field_errors,omitempty"`
}

// ErrorEnvelope wraps every error response.
type ErrorEnvelope struct {
	Error ErrorBodyDTO `json:"error"`
}

// --- auth ---

// AuthTokenResponse is the register/login data payload.
type AuthTokenResponse struct {
	Token     string      `json:"token"`
	TokenType string      `json:"token_type"`
	ExpiresIn int64       `json:"expires_in"`
	User      AuthUserDTO `json:"user"`
}

// AuthUserDTO is the token-issue user summary.
type AuthUserDTO struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// MeResponse is the GET /auth/me data payload.
type MeResponse struct {
	User UserProfileDTO `json:"user"`
}

// UserProfileDTO is the aggregated IAM + Organization profile.
type UserProfileDTO struct {
	ID                   int64          `json:"id"`
	Username             string         `json:"username"`
	Account              string         `json:"account"`
	Email                string         `json:"email"`
	CreatedAt            time.Time      `json:"created_at"`
	Department           *DepartmentDTO `json:"department"` // MUST be JSON null without membership
	Roles                []RoleDTO      `json:"roles"`
	EffectivePermissions []string       `json:"effective_permissions"` // sorted unique, [] not null
}

// DepartmentDTO is the public department reference.
type DepartmentDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RoleDTO is the public role reference inside a profile.
type RoleDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// --- roles ---

// RoleItemDTO is one GET /roles list item.
type RoleItemDTO struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	IsBuiltin bool      `json:"is_builtin"`
	CreatedAt time.Time `json:"created_at"`
}

// RolesResponse is the GET /roles data payload.
type RolesResponse struct {
	List  []RoleItemDTO `json:"list"`
	Total int64         `json:"total"`
}

// --- departments ---

// DepartmentTreeDTO is one nested GET /departments node; Children is [].
type DepartmentTreeDTO struct {
	ID       int64               `json:"id"`
	Name     string              `json:"name"`
	ParentID *int64              `json:"parent_id"`
	Children []DepartmentTreeDTO `json:"children"`
}

// DepartmentsResponse is the GET /departments data payload.
type DepartmentsResponse struct {
	List []DepartmentTreeDTO `json:"list"`
}

// --- users ---

// UserItemDTO is one GET /users list item. Role is the comma-separated role
// name string for current frontend compatibility.
type UserItemDTO struct {
	ID         int64          `json:"id"`
	Username   string         `json:"username"`
	Account    string         `json:"account"`
	Email      string         `json:"email"`
	CreateTime time.Time      `json:"create_time"`
	Role       string         `json:"role"`
	Department *DepartmentDTO `json:"department"` // MUST be JSON null without membership
}

// UsersResponse is the GET /users data payload.
type UsersResponse struct {
	List  []UserItemDTO `json:"list"`
	Total int64         `json:"total"`
}
