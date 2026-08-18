package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"vue-element-plus-admin/backend/internal/dto/response"
	"vue-element-plus-admin/backend/internal/modules/permission"

	"github.com/gin-gonic/gin"
)

type currentUserServiceStub struct {
	user   *CurrentUser
	err    error
	userID int64
}

type currentUserMenuServiceStub struct {
	menus         []permission.MenuItem
	roleCodes     []string
	administrator bool
}

func (s *currentUserMenuServiceStub) AuthorizedTree(
	_ context.Context,
	roleCodes []string,
	administrator bool,
) ([]permission.MenuItem, error) {
	s.roleCodes = roleCodes
	s.administrator = administrator
	return s.menus, nil
}

func (s *currentUserServiceStub) GetUserByID(_ context.Context, userID int64) (*CurrentUser, error) {
	s.userID = userID
	return s.user, s.err
}

func TestGetCurrentUserMenus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userService := &currentUserServiceStub{user: &CurrentUser{
		ID: 1, RoleID: 2, RoleCode: "admin", RoleCodes: []string{"admin"},
		Permissions: []string{"*.*.*"}, IsActive: true,
	}}
	menuService := &currentUserMenuServiceStub{menus: []permission.MenuItem{
		{ID: 9, Path: "/dashboard", Status: 1},
	}}
	handler := NewUserHandler(userService, menuService)
	router := gin.New()
	router.GET("/me/menus", func(c *gin.Context) {
		c.Set("userID", int64(1))
		handler.GetCurrentUserMenus(c)
	})

	request := httptest.NewRequest(http.MethodGet, "/me/menus", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(menuService.roleCodes) != 1 || menuService.roleCodes[0] != "admin" || !menuService.administrator {
		t.Fatalf("menu lookup roles = %v, administrator = %v", menuService.roleCodes, menuService.administrator)
	}
	var body struct {
		Data struct {
			List []permission.MenuItem `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data.List) != 1 || body.Data.List[0].ID != 9 {
		t.Fatalf("menu response = %#v", body.Data.List)
	}
}

func TestGetCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("returns wrapped current user", func(t *testing.T) {
		service := &currentUserServiceStub{user: &CurrentUser{
			ID:          1,
			Username:    "admin",
			RoleID:      2,
			RoleCode:    "admin",
			RoleName:    "管理员",
			Permissions: []string{"user:add", "user:edit"},
			IsActive:    true,
		}}
		handler := NewUserHandler(service, nil)
		router := gin.New()
		router.GET("/me", func(c *gin.Context) {
			c.Set("userID", int64(1))
			handler.GetCurrentUser(c)
		})

		request := httptest.NewRequest(http.MethodGet, "/me", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		var body struct {
			Code    int               `json:"code"`
			Data    response.UserInfo `json:"data"`
			Message string            `json:"message"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Code != 0 || body.Message != "success" {
			t.Fatalf("response envelope = %#v", body)
		}
		if body.Data.RoleCode != "admin" || len(body.Data.Permissions) != 2 {
			t.Fatalf("response user = %#v", body.Data)
		}
		if service.userID != 1 {
			t.Fatalf("service user ID = %d, want 1", service.userID)
		}
	})

	t.Run("uses unified disabled response", func(t *testing.T) {
		service := &currentUserServiceStub{err: ErrUserDisabled}
		handler := NewUserHandler(service, nil)
		router := gin.New()
		router.GET("/me", func(c *gin.Context) {
			c.Set("userID", int64(2))
			handler.GetCurrentUser(c)
		})

		request := httptest.NewRequest(http.MethodGet, "/me", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
		var body response.Response
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Code != 40301 {
			t.Fatalf("code = %d, want 40301", body.Code)
		}
	})

	t.Run("does not expose internal errors", func(t *testing.T) {
		service := &currentUserServiceStub{err: errors.New("database details")}
		handler := NewUserHandler(service, nil)
		router := gin.New()
		router.GET("/me", func(c *gin.Context) {
			c.Set("userID", int64(1))
			handler.GetCurrentUser(c)
		})

		request := httptest.NewRequest(http.MethodGet, "/me", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if recorder.Body.String() == "database details" {
			t.Fatal("internal error was exposed")
		}
	})
}
