package gmailapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
	jwtservice "vue-element-plus-admin/backend/internal/middleware/jwt"
	oidcCustom "vue-element-plus-admin/backend/internal/middleware/oidc"
	"vue-element-plus-admin/backend/internal/security"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type tokenRepositoryStub struct {
	row      *db.GetGoogleTokenByUserIDRow
	getErr   error
	added    db.AddGoogleTokenParams
	addCalls int
}

func (r *tokenRepositoryStub) GetToken(
	context.Context,
	int64,
) (*db.GetGoogleTokenByUserIDRow, error) {
	return r.row, r.getErr
}

func (r *tokenRepositoryStub) AddToken(
	_ context.Context,
	params db.AddGoogleTokenParams,
) error {
	r.added = params
	r.addCalls++
	return nil
}

func TestListUnreadMessageDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/messages":
			if r.URL.Query().Get("labelIds") != "INBOX" ||
				r.URL.Query().Get("q") != "is:unread" ||
				r.URL.Query().Get("maxResults") != "10" {
				t.Errorf("unexpected list query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{
				"messages":[{"id":"message-1","threadId":"thread-1"}],
				"nextPageToken":"next-page",
				"resultSizeEstimate":4
			}`))
		case "/messages/message-1":
			if r.URL.Query().Get("format") != "full" {
				t.Errorf("format = %q, want full", r.URL.Query().Get("format"))
			}
			_, _ = w.Write([]byte(`{
				"id":"message-1",
				"threadId":"thread-1",
				"labelIds":["INBOX","UNREAD"],
				"snippet":"hello",
				"payload":{
					"mimeType":"multipart/alternative",
					"headers":[{"name":"Subject","value":"Test subject"}],
					"parts":[{
						"partId":"0",
						"mimeType":"multipart/mixed",
						"parts":[{"partId":"0.1","mimeType":"text/plain","body":{"size":5,"data":"aGVsbG8"}}]
					}]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := listUnreadMessageDetails(
		context.Background(),
		server.Client(),
		server.URL+"/messages",
		"access-token",
	)
	if err != nil {
		t.Fatalf("listUnreadMessageDetails() error = %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != "message-1" {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if got.NextPageToken != "next-page" || got.ResultSizeEstimate != 4 {
		t.Fatalf("list metadata = (%q, %d)", got.NextPageToken, got.ResultSizeEstimate)
	}
	parts := got.Messages[0].Payload.Parts
	if len(parts) != 1 || len(parts[0].Parts) != 1 || parts[0].Parts[0].MimeType != "text/plain" {
		t.Fatalf("nested MIME parts were not decoded: %+v", parts)
	}
}

func TestListUnreadMessageDetailsFetchesDetailsConcurrently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/messages" {
			_, _ = w.Write([]byte(`{
				"messages":[
					{"id":"1"},{"id":"2"},{"id":"3"},
					{"id":"4"},{"id":"5"},{"id":"6"}
				]
			}`))
			return
		}

		time.Sleep(120 * time.Millisecond)
		messageID := strings.TrimPrefix(r.URL.Path, "/messages/")
		_, _ = fmt.Fprintf(w, `{"id":%q,"payload":{"headers":[],"body":{}}}`, messageID)
	}))
	defer server.Close()

	startedAt := time.Now()
	got, err := listUnreadMessageDetails(
		context.Background(),
		server.Client(),
		server.URL+"/messages",
		"access-token",
	)
	if err != nil {
		t.Fatalf("listUnreadMessageDetails() error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 600*time.Millisecond {
		t.Fatalf("details took %s; requests appear to be sequential", elapsed)
	}
	for index, message := range got.Messages {
		wantID := fmt.Sprintf("%d", index+1)
		if message.ID != wantID {
			t.Fatalf("message[%d].ID = %q, want %q", index, message.ID, wantID)
		}
	}
}

func TestRefreshGoogleTokenForcesRefreshOfUnexpiredToken(t *testing.T) {
	var refreshRequests int
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshRequests++
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Errorf("refresh form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	key := []byte("0123456789abcdef0123456789abcdef")
	accessEncrypted, err := security.Encrypt(key, []byte("old-access"))
	if err != nil {
		t.Fatalf("encrypt access token: %v", err)
	}
	refreshEncrypted, err := security.Encrypt(key, []byte("old-refresh"))
	if err != nil {
		t.Fatalf("encrypt refresh token: %v", err)
	}
	repository := &tokenRepositoryStub{row: &db.GetGoogleTokenByUserIDRow{
		AccessTokenEncrypted:  string(accessEncrypted),
		RefreshTokenEncrypted: string(refreshEncrypted),
		Expiry:                time.Now().Add(time.Hour),
		ProviderSubject:       "google-subject",
		TokenType:             "Bearer",
	}}
	service := NewGmailService(repository, &oidcCustom.OIDCAuth{OauthConfig: &oauth2.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL:  tokenServer.URL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes: []string{"gmail.readonly"},
	}}, key)

	got, err := service.RefreshGoogleToken(context.Background(), 42)
	if err != nil {
		t.Fatalf("RefreshGoogleToken() error = %v", err)
	}
	if refreshRequests != 1 || got.AccessToken != "new-access" {
		t.Fatalf("refresh requests = %d, token = %+v", refreshRequests, got)
	}
	if got.RefreshToken != "old-refresh" {
		t.Fatalf("RefreshToken = %q, want preserved old-refresh", got.RefreshToken)
	}
	if repository.addCalls != 1 || repository.added.RefreshTokenEncrypted != "" {
		t.Fatalf("persisted refresh state = (%d, %q)", repository.addCalls, repository.added.RefreshTokenEncrypted)
	}
	storedAccess, err := security.Decrypt(key, []byte(repository.added.AccessTokenEncrypted))
	if err != nil || string(storedAccess) != "new-access" {
		t.Fatalf("stored access token = %q, err = %v", storedAccess, err)
	}
}

func TestGetTokenReportsGoogleNotConnected(t *testing.T) {
	service := NewGmailService(&tokenRepositoryStub{getErr: sql.ErrNoRows}, nil, nil)
	_, err := service.GetToken(context.Background(), 42)
	if !errors.Is(err, ErrGoogleNotConnected) {
		t.Fatalf("GetToken() error = %v, want %v", err, ErrGoogleNotConnected)
	}
}

func TestListUnreadMessagesReturnsConflictWhenGoogleIsNotConnected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := NewGmailService(&tokenRepositoryStub{getErr: sql.ErrNoRows}, nil, nil)
	router := gin.New()
	router.GET("/gmail/unread", func(c *gin.Context) {
		c.Set(jwtservice.UserIDContextKey, int64(42))
		NewGmailHandler(service).ListUnreadMessages(c)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/gmail/unread", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != 40901 {
		t.Fatalf("code = %d, want 40901", body.Code)
	}
}

func TestGmailRouteRequiresSystemAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	GmailRouter(router.Group("/api/v1"), &GmailHandler{}, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/gmail/unread", nil))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "authorization") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
