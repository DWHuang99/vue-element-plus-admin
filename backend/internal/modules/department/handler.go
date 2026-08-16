package department

import (
	"context"
	"errors"
	"net/http"

	"vue-element-plus-admin/backend/internal/dto/response"
	"vue-element-plus-admin/backend/internal/modules/permission"

	"github.com/gin-gonic/gin"
)

type DepartmentService interface {
	Tree(ctx context.Context) ([]DepartmentItem, error)
	List(ctx context.Context, page, size int, name string) ([]DepartmentItem, int, error)
	Create(ctx context.Context, input Input) error
	Update(ctx context.Context, id int64, input Input) error
	Delete(ctx context.Context, ids []int64) error
}

type Handler struct {
	service DepartmentService
}

func NewHandler(service DepartmentService) *Handler {
	return &Handler{service: service}
}

func (h *Handler) DepartmentTree(c *gin.Context) {
	items, err := h.service.Tree(c.Request.Context())
	if err != nil {
		permission.WriteError(c, http.StatusInternalServerError, "failed to load departments")
		return
	}
	response.Success(c, gin.H{"list": items})
}

func (h *Handler) DepartmentList(c *gin.Context) {
	page, size := permission.ParsePage(c)
	items, total, err := h.service.List(c.Request.Context(), page, size, c.Query("departmentName"))
	if err != nil {
		permission.WriteError(c, http.StatusInternalServerError, "failed to load departments")
		return
	}
	response.Success(c, gin.H{"list": items, "total": total})
}

func (h *Handler) DepartmentCreate(c *gin.Context) {
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Create(c.Request.Context(), input); err != nil {
		if errors.Is(err, ErrInvalidInput) {
			permission.WriteError(c, http.StatusBadRequest, "departmentName is required")
			return
		}
		permission.WriteError(c, http.StatusInternalServerError, "failed to create department")
		return
	}
	response.Success(c, nil)
}

func (h *Handler) DepartmentUpdate(c *gin.Context) {
	id, err := permission.ParseID(c.Param("id"))
	if err != nil {
		permission.WriteError(c, http.StatusBadRequest, "invalid department id")
		return
	}
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Update(c.Request.Context(), id, input); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "invalid department input")
		case errors.Is(err, ErrNotFound):
			permission.WriteError(c, http.StatusNotFound, "department not found")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to update department")
		}
		return
	}
	response.Success(c, nil)
}

func (h *Handler) DepartmentDelete(c *gin.Context) {
	ids, ok := permission.ParseIDs(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), ids); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "invalid department ids")
		case errors.Is(err, ErrHasChildren):
			permission.WriteError(c, http.StatusConflict, "department has children")
		case errors.Is(err, ErrHasUsers):
			permission.WriteError(c, http.StatusConflict, "department has users")
		case errors.Is(err, ErrDeletionInProgress):
			permission.WriteError(c, http.StatusConflict, "department deletion is already in progress")
		case errors.Is(err, ErrNotFound):
			permission.WriteError(c, http.StatusNotFound, "department not found")
		case errors.Is(err, ErrUserServiceUnavailable):
			permission.WriteError(c, http.StatusServiceUnavailable, "user service unavailable")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to delete departments")
		}
		return
	}
	response.Success(c, nil)
}
