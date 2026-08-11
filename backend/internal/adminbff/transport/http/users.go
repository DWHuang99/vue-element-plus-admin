// BFF user handlers. The read side (GET /users) is the T029 composition; the
// write endpoints run the managed-user workflow sagas (T056): POST /users
// creates (no id) or updates (id present), POST /users/delete runs the batch
// delete saga — each scoped by the validated Idempotency-Key header
// (contracts/http-api-compatibility.md Managed-user idempotency header).
// Write success stays 200 {"data":{}}.
package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
)

// ListUsers handles GET /api/v1/users: department filter first (Organization
// candidate set), then IAM filter/pagination, then one batch department
// resolution for the current page.
func (h *Handler) ListUsers(c *gin.Context) {
	params := adminbff.ListManagedUsersParams{
		Username:  c.Query("username"),
		Account:   c.Query("account"),
		PageIndex: queryInt(c, "page_index", 1),
		PageSize:  queryInt(c, "page_size", 10),
	}
	if v := queryInt64(c, "department_id", 0); v > 0 {
		params.DepartmentID = &v
	}

	result, err := h.svc.ListManagedUsers(c.Request.Context(), params)
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	list := make([]UserItemDTO, 0, len(result.List))
	for _, item := range result.List {
		dto := UserItemDTO{
			ID:         item.ID,
			Username:   item.Username,
			Account:    strOrEmpty(item.Account),
			Email:      strOrEmpty(item.Email),
			CreateTime: item.CreateTime,
			Role:       item.Role,
		}
		if item.Department != nil {
			dto.Department = &DepartmentDTO{ID: item.Department.ID, Name: item.Department.Name}
		}
		list = append(list, dto)
	}
	writeData(c, http.StatusOK, UsersResponse{List: list, Total: result.Total})
}

// SaveUserWorkflow handles POST /api/v1/users (T056): the request creates
// (no id) or updates (id present) through the managed-user saga. The
// Idempotency-Key header (validated by the IdempotencyKey middleware) scopes
// the logical operation; a same-key replay/retry returns or continues the
// same workflow instead of duplicating side effects. Password is credential
// material: it lives in this request only.
func (h *Handler) SaveUserWorkflow(c *gin.Context) {
	var req saveUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, h.logger, adminbff.ErrInvalidInput)
		return
	}
	if fieldErrs := validateUser(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, ErrorEnvelope{Error: ErrorBodyDTO{
			Code: "AUTH_INVALID_INPUT", Message: "请求参数校验失败", FieldErrors: fieldErrs,
		}})
		return
	}
	if _, ok := principalFromContext(c); !ok {
		writeError(c, h.logger, adminbff.ErrUnauthorized)
		return
	}

	op := requestContext(c)
	var password string
	if req.Password != nil {
		password = *req.Password
	}

	if req.ID == nil {
		if _, err := h.svc.CreateUser(c.Request.Context(), op, adminbff.CreateUserParams{
			Username:     strings.TrimSpace(req.Username),
			Account:      optionalString(req.Account),
			Email:        optionalString(req.Email),
			Password:     password,
			RoleIDs:      req.Roles,
			DepartmentID: req.DepartmentID,
		}); err != nil {
			writeError(c, h.logger, err)
			return
		}
	} else {
		if _, err := h.svc.UpdateUser(c.Request.Context(), op, adminbff.UpdateUserParams{
			UserID:       *req.ID,
			Username:     strings.TrimSpace(req.Username),
			Account:      optionalString(req.Account),
			Email:        optionalString(req.Email),
			Password:     password,
			RoleIDs:      req.Roles,
			DepartmentID: req.DepartmentID,
		}); err != nil {
			writeError(c, h.logger, err)
			return
		}
	}
	writeData(c, http.StatusOK, EmptyData{})
}

// DeleteUsersWorkflow handles POST /api/v1/users/delete (T056): the batch
// delete saga (D0–D5) with the same Idempotency-Key scope; a same-key retry
// after an unknown outcome replays the committed deletion without duplicate
// events. Success stays 200 {"data":{}}.
func (h *Handler) DeleteUsersWorkflow(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, h.logger, adminbff.ErrInvalidInput)
		return
	}
	if fieldErrs := validateIDs(req.IDs); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, ErrorEnvelope{Error: ErrorBodyDTO{
			Code: "AUTH_INVALID_INPUT", Message: "请求参数校验失败", FieldErrors: fieldErrs,
		}})
		return
	}
	if _, ok := principalFromContext(c); !ok {
		writeError(c, h.logger, adminbff.ErrUnauthorized)
		return
	}

	if _, err := h.svc.DeleteUsers(c.Request.Context(), requestContext(c), adminbff.DeleteUserParams{UserIDs: req.IDs}); err != nil {
		writeError(c, h.logger, err)
		return
	}
	writeData(c, http.StatusOK, EmptyData{})
}

// optionalString maps an empty form value to nil — the contract fingerprints
// optional fields as explicit JSON null, so an absent account/email must not
// enter the request identity as "".
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
