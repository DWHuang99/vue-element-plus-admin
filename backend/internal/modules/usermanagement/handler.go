package usermanagement

import (
	"context"
	"errors"
	"net/http"

	"vue-element-plus-admin/backend/internal/dto/response"
	"vue-element-plus-admin/backend/internal/modules/permission"

	"github.com/gin-gonic/gin"
)

type ApplicationService interface {
	List(ctx context.Context, filter Filter) ([]UserItem, int, error)
	Create(ctx context.Context, input Input) error
	Update(ctx context.Context, id int64, input Input) error
	Delete(ctx context.Context, ids []int64) error
}

type Handler struct {
	service ApplicationService
}

func NewHandler(service ApplicationService) *Handler {
	return &Handler{service: service}
}

func (h *Handler) UserList(c *gin.Context) {
	page, size := permission.ParsePage(c)
	departmentIDValue := c.Query("departmentId")
	if departmentIDValue == "" {
		departmentIDValue = c.Query("id")
	}
	departmentID := int64(0)
	if departmentIDValue != "" {
		var err error
		departmentID, err = permission.ParseID(departmentIDValue)
		if err != nil {
			permission.WriteError(c, http.StatusBadRequest, "invalid department id")
			return
		}
	}
	items, total, err := h.service.List(c.Request.Context(), Filter{
		Page:         page,
		Size:         size,
		DepartmentID: departmentID,
		Username:     c.Query("username"),
		Account:      c.Query("account"),
	})
	if err != nil {
		if errors.Is(err, ErrDepartmentUnavailable) {
			permission.WriteError(c, http.StatusServiceUnavailable, "department service unavailable")
			return
		}
		permission.WriteError(c, http.StatusInternalServerError, "failed to load users")
		return
	}
	response.Success(c, gin.H{"list": items, "total": total})
}

func (h *Handler) UserCreate(c *gin.Context) {
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Create(c.Request.Context(), input); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "username, password and roleId are required")
		case errors.Is(err, ErrUserConflict):
			permission.WriteError(c, http.StatusConflict, "username already exists or role is invalid")
		case errors.Is(err, ErrDepartmentNotFound):
			permission.WriteError(c, http.StatusBadRequest, "department not found")
		case errors.Is(err, ErrDepartmentDisabled):
			permission.WriteError(c, http.StatusBadRequest, "department is disabled")
		case errors.Is(err, ErrDepartmentDeleting):
			permission.WriteError(c, http.StatusConflict, "department is being deleted")
		case errors.Is(err, ErrDepartmentUnavailable):
			permission.WriteError(c, http.StatusServiceUnavailable, "department service unavailable")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "failed to create user")
		}
		return
	}
	response.Success(c, nil)
}

func (h *Handler) UserUpdate(c *gin.Context) {
	id, err := permission.ParseID(c.Param("id"))
	if err != nil {
		permission.WriteError(c, http.StatusBadRequest, "invalid user id")
		return
	}
	var input Input
	if !permission.BindJSON(c, &input) {
		return
	}
	if err := h.service.Update(c.Request.Context(), id, input); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			permission.WriteError(c, http.StatusBadRequest, "invalid user input")
		case errors.Is(err, ErrUserNotFound):
			permission.WriteError(c, http.StatusNotFound, "user not found")
		case errors.Is(err, ErrUserConflict):
			permission.WriteError(c, http.StatusConflict, "user update failed")
		case errors.Is(err, ErrDepartmentNotFound):
			permission.WriteError(c, http.StatusBadRequest, "department not found")
		case errors.Is(err, ErrDepartmentDisabled):
			permission.WriteError(c, http.StatusBadRequest, "department is disabled")
		case errors.Is(err, ErrDepartmentDeleting):
			permission.WriteError(c, http.StatusConflict, "department is being deleted")
		case errors.Is(err, ErrDepartmentUnavailable):
			permission.WriteError(c, http.StatusServiceUnavailable, "department service unavailable")
		default:
			permission.WriteError(c, http.StatusInternalServerError, "user update failed")
		}
		return
	}
	response.Success(c, nil)
}

func (h *Handler) UserDelete(c *gin.Context) {
	ids, ok := permission.ParseIDs(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), ids); err != nil {
		if errors.Is(err, ErrInvalidInput) {
			permission.WriteError(c, http.StatusBadRequest, "invalid user ids")
			return
		}
		permission.WriteError(c, http.StatusInternalServerError, "failed to delete users")
		return
	}
	response.Success(c, nil)
}
