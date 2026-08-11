// BFF department handlers (T027): GET/POST /departments and POST
// /departments/delete through the Organization ports, with stable codes
// DEPARTMENT_NOT_FOUND / NAME_TAKEN / DELETE_PROTECTED.
package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// ListDepartments handles GET /api/v1/departments: the deterministic tree
// with Children always [] (never null).
func (h *Handler) ListDepartments(c *gin.Context) {
	tree, err := h.depts.ListDepartmentTree(c.Request.Context())
	if err != nil {
		writeError(c, h.logger, err)
		return
	}
	writeData(c, http.StatusOK, DepartmentsResponse{List: toDepartmentTreeDTOs(tree)})
}

// SaveDepartment handles POST /api/v1/departments.
func (h *Handler) SaveDepartment(c *gin.Context) {
	var req saveDepartmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, h.logger, iam.ErrInvalidInput)
		return
	}
	if fieldErrs := validateDepartment(req); len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, ErrorEnvelope{Error: ErrorBodyDTO{
			Code: "AUTH_INVALID_INPUT", Message: "请求参数校验失败", FieldErrors: fieldErrs,
		}})
		return
	}
	if _, err := h.depts.SaveDepartment(c.Request.Context(), orgOperationContext(c),
		req.ID, req.Name, req.ParentID); err != nil {
		writeError(c, h.logger, err)
		return
	}
	writeData(c, http.StatusOK, EmptyData{})
}

// DeleteDepartments handles POST /api/v1/departments/delete.
func (h *Handler) DeleteDepartments(c *gin.Context) {
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
	if err := h.depts.DeleteDepartments(c.Request.Context(), orgOperationContext(c), req.IDs); err != nil {
		writeError(c, h.logger, err)
		return
	}
	writeData(c, http.StatusOK, EmptyData{})
}

// orgOperationContext builds the Organization port context from the
// validated request ID (T073); US2 receipts reuse the same shape.
func orgOperationContext(c *gin.Context) organization.OperationContext {
	return orgRequestContext(c)
}

// toDepartmentTreeDTOs maps the application tree to the public tree; the
// application guarantees Children is non-nil, mirrored here.
func toDepartmentTreeDTOs(nodes []organization.DepartmentNode) []DepartmentTreeDTO {
	out := make([]DepartmentTreeDTO, 0, len(nodes))
	for _, n := range nodes {
		dto := DepartmentTreeDTO{
			ID:       n.ID,
			Name:     n.Name,
			ParentID: n.ParentID,
		}
		dto.Children = toDepartmentTreeDTOs(n.Children)
		if dto.Children == nil {
			dto.Children = []DepartmentTreeDTO{}
		}
		out = append(out, dto)
	}
	return out
}
