package role

import (
	"context"
	"errors"
	"net/http"

	"vue-element-plus-admin/backend/internal/dto/response"
	"vue-element-plus-admin/backend/internal/modules/permission"

	"github.com/gin-gonic/gin"
)

type RoleService interface {
	List(ctx context.Context, page, size int, name string) ([]RoleItem, int, error)
	Get(ctx context.Context, id int64) (RoleItem, error)
	Create(ctx context.Context, input Input) error
	Update(ctx context.Context, id int64, input Input) error
	Delete(ctx context.Context, id int64) error
}

type Handler struct {
	service RoleService
}

func NewHandler(service RoleService) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RoleList(c *gin.Context) {
	page, size := permission.ParsePage(c)
	items, total, err := h.service.List(c.Request.Context(), page, size, c.Query("roleName"))
	if err != nil {
		permission.WriteError(c, http.StatusInternalServerError, "failed to load roles")
		return
	}
	response.Success(c, gin.H{"list": items, "total": total})
}

func (h *Handler) RoleDetail(c *gin.Context) {
	id, err := permission.ParseID(c.Param("id"))
	if err != nil {
		permission.WriteError(c, http.StatusBadRequest, "invalid role id")
		return
	}
	item, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "invalid role id")
		case errors.Is(err, ErrNotFound):
			permission.WriteError(c, http.StatusNotFound, "role not found")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to load role")
		}
		return
	}
	response.Success(c, item)
}

func (h *Handler) RoleCreate(c *gin.Context) {
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Create(c.Request.Context(), input); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "roleName is required")
		case errors.Is(err, ErrRoleCodeConflict):
			permission.WriteError(c, http.StatusConflict, "role code already exists")
		case errors.Is(err, ErrInvalidAccess):
			permission.WriteError(c, http.StatusBadRequest, "invalid menu or permission assignment")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to create role")
		}
		return
	}
	response.Success(c, nil)
}

func (h *Handler) RoleUpdate(c *gin.Context) {
	id, err := permission.ParseID(c.Param("id"))
	if err != nil {
		permission.WriteError(c, http.StatusBadRequest, "invalid role id")
		return
	}
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Update(c.Request.Context(), id, input); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrInvalidAccess):
			permission.WriteError(c, http.StatusBadRequest, "invalid role input")
		case errors.Is(err, ErrNotFound):
			permission.WriteError(c, http.StatusNotFound, "role not found")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "role update failed")
		}
		return
	}
	response.Success(c, nil)
}

func (h *Handler) RoleDelete(c *gin.Context) {
	id, err := permission.ParseID(c.Param("id"))
	if err != nil {
		permission.WriteError(c, http.StatusBadRequest, "invalid role id")
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		switch {
		case errors.Is(err, ErrAssignedUsers):
			permission.WriteError(c, http.StatusConflict, "role is assigned to users")
		case errors.Is(err, ErrNotFound):
			permission.WriteError(c, http.StatusNotFound, "role not found")
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "invalid role id")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to delete role")
		}
		return
	}
	response.Success(c, nil)
}
