package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedirectToFrontendSupportsHashHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewOauthHandler(nil, nil, false, "http://localhost:4000/#/login")
	router := gin.New()
	router.GET("/callback", func(c *gin.Context) {
		handler.redirectToFrontend(c, "error", "invalid_state")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/callback", nil))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	want := "http://localhost:4000/#/login?error=invalid_state&oauth=error"
	if location := recorder.Header().Get("Location"); location != want {
		t.Fatalf("Location = %q, want %q", location, want)
	}
}
