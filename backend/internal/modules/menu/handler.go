package menu

import (
	"context"
	"errors"
	"net/http"

	"vue-element-plus-admin/backend/internal/dto/response"
	"vue-element-plus-admin/backend/internal/modules/permission"

	"github.com/gin-gonic/gin"
)

type MenuService interface {
	Tree(ctx context.Context) ([]MenuItem, error)
	Create(ctx context.Context, input Input) error
	Update(ctx context.Context, id int64, input Input) error
	Delete(ctx context.Context, id int64) error
}

type Handler struct {
	service MenuService
}

func NewHandler(service MenuService) *Handler {
	return &Handler{service: service}
}

func (h *Handler) MenuTree(c *gin.Context) {
	items, err := h.service.Tree(c.Request.Context())
	if err != nil {
		permission.WriteError(c, http.StatusInternalServerError, "failed to load menus")
		return
	}
	response.Success(c, gin.H{"list": items})
}

func (h *Handler) MenuCreate(c *gin.Context) {
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Create(c.Request.Context(), input); err != nil {
		if errors.Is(err, ErrInvalidInput) {
			permission.WriteError(c, http.StatusBadRequest, "path and meta.title are required")
			return
		}
		permission.WriteError(c, http.StatusInternalServerError, "failed to create menu")
		return
	}
	response.Success(c, nil)
}

func (h *Handler) MenuUpdate(c *gin.Context) {
	id, err := permission.ParseID(c.Param("id"))
	if err != nil {
		permission.WriteError(c, http.StatusBadRequest, "invalid menu id")
		return
	}
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Update(c.Request.Context(), id, input); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "invalid menu input")
		case errors.Is(err, ErrNotFound):
			permission.WriteError(c, http.StatusNotFound, "menu not found")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to update menu")
		}
		return
	}
	response.Success(c, nil)
}

func (h *Handler) MenuDelete(c *gin.Context) {
	id, err := permission.ParseID(c.Param("id"))
	if err != nil {
		permission.WriteError(c, http.StatusBadRequest, "invalid menu id")
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "invalid menu id")
		case errors.Is(err, ErrHasChildren):
			permission.WriteError(c, http.StatusConflict, "menu has children")
		case errors.Is(err, ErrNotFound):
			permission.WriteError(c, http.StatusNotFound, "menu not found")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to delete menu")
		}
		return
	}
	response.Success(c, nil)
}
