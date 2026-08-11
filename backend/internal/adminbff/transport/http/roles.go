// BFF role handlers (T026): GET/POST /roles and POST /roles/delete with
// stable codes ROLE_NOT_FOUND / NAME_TAKEN / BUILTIN_ROLE_CODE_IMMUTABLE /
// BUILTIN_ROLE_DELETE_PROTECTED / DELETE_PROTECTED.
package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
)

// ListRoles handles GET /api/v1/roles.
func (h *Handler) ListRoles(c *gin.Context) {
	roles, err := h.roles.ListRoles(c.Request.Context())
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	list := make([]RoleItemDTO, 0, len(roles))
	for _, r := range roles {
		list = append(list, RoleItemDTO{
			ID:        r.ID,
			Name:      r.Name,
			Code:      r.Code,
			IsBuiltin: r.IsBuiltin,
			CreatedAt: r.CreatedAt,
		})
	}
	writeData(c, http.StatusOK, RolesResponse{List: list, Total: int64(len(list))})
}

// SaveRole handles POST /api/v1/roles.
func (h *Handler) SaveRole(c *gin.Context) {
	var req saveRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, h.logger, iam.ErrInvalidInput)
		return
	}
	if fieldErrs := validateRole(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, ErrorEnvelope{Error: ErrorBodyDTO{
			Code: "AUTH_INVALID_INPUT", Message: "请求参数校验失败", FieldErrors: fieldErrs,
		}})
		return
	}
	if _, err := h.roles.SaveRole(c.Request.Context(), operationContext(c), req.ID, req.Name, req.Code); err != nil {
		writeError(c, h.logger, err)
		return
	}
	writeData(c, http.StatusOK, EmptyData{})
}

// DeleteRoles handles POST /api/v1/roles/delete.
func (h *Handler) DeleteRoles(c *gin.Context) {
	var req deleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, h.logger, iam.ErrInvalidInput)
		return
	}
	if fieldErrs := validateIDs(req.IDs); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, ErrorEnvelope{Error: ErrorBodyDTO{
			Code: "AUTH_INVALID_INPUT", Message: "请求参数校验失败", FieldErrors: fieldErrs,
		}})
		return
	}
	if err := h.roles.DeleteRoles(c.Request.Context(), operationContext(c), req.IDs); err != nil {
		writeError(c, h.logger, err)
		return
	}
	writeData(c, http.StatusOK, EmptyData{})
}
