package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"vue-element-plus-admin/backend/internal/dto/response"

	"github.com/gin-gonic/gin"
)

type currentUserServiceStub struct {
	user     *CurrentUser
	err      error
	username string
}

func (s *currentUserServiceStub) GetUserByUsername(_ context.Context, username string) (*CurrentUser, error) {
	s.username = username
	return s.user, s.err
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
		handler := NewUserHandler(service)
		router := gin.New()
		router.GET("/me", func(c *gin.Context) {
			c.Set("username", "admin")
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
		if service.username != "admin" {
			t.Fatalf("service username = %q, want admin", service.username)
		}
	})

	t.Run("uses unified disabled response", func(t *testing.T) {
		service := &currentUserServiceStub{err: ErrUserDisabled}
		handler := NewUserHandler(service)
		router := gin.New()
		router.GET("/me", func(c *gin.Context) {
			c.Set("username", "disabled")
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
		handler := NewUserHandler(service)
		router := gin.New()
		router.GET("/me", func(c *gin.Context) {
			c.Set("username", "admin")
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
