package oauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"time"

	"vue-element-plus-admin/backend/internal/dto/response"
	oidcCustom "vue-element-plus-admin/backend/internal/middleware/oidc"
	"vue-element-plus-admin/backend/internal/modules/auth"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type OauthHandler struct {
	service             *ExternalUserService
	authservice         *auth.AuthService
	cookieSecure        bool
	frontendRedirectURL string
}

func NewOauthHandler(
	service *ExternalUserService,
	authservice *auth.AuthService,
	cookieSecure bool,
	frontendRedirectURL string,
) *OauthHandler {
	return &OauthHandler{
		service:             service,
		authservice:         authservice,
		cookieSecure:        cookieSecure,
		frontendRedirectURL: frontendRedirectURL,
	}
}

func (h *OauthHandler) redirectToFrontend(c *gin.Context, status string, errorCode string) {
	redirectURL, err := url.Parse(h.frontendRedirectURL)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, 10500, "invalid OIDC frontend redirect URL")
		return
	}

	queryTarget := redirectURL
	if redirectURL.Fragment != "" {
		queryTarget, err = url.Parse(redirectURL.Fragment)
		if err != nil {
			response.Error(c, http.StatusInternalServerError, 10500, "invalid OIDC frontend redirect URL")
			return
		}
	}

	query := queryTarget.Query()
	query.Set("oauth", status)
	if errorCode != "" {
		query.Set("error", errorCode)
	}
	queryTarget.RawQuery = query.Encode()
	if redirectURL.Fragment != "" {
		redirectURL.Fragment = queryTarget.String()
	} else {
		redirectURL.RawQuery = queryTarget.RawQuery
	}
	c.Redirect(http.StatusFound, redirectURL.String())
}

const (
	refreshCookiePath       = "/api/v1/auth"
	legacyRefreshCookiePath = "/api/v1/auth/refresh"
)

func (h *OauthHandler) expireRefreshCookie(c *gin.Context, path string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("refresh_token", "", -1, path, "", h.cookieSecure, true)
}

func (h *OauthHandler) setRefreshCookie(c *gin.Context, refreshToken string) {
	// Remove cookies created by versions that scoped the token to /refresh only.
	h.expireRefreshCookie(c, legacyRefreshCookiePath)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(
		"refresh_token",
		refreshToken,
		int(h.authservice.RefreshTTL().Seconds()),
		refreshCookiePath,
		"",
		h.cookieSecure,
		true,
	)
}

func (h *OauthHandler) clearRefreshCookie(c *gin.Context) {
	h.expireRefreshCookie(c, refreshCookiePath)
	h.expireRefreshCookie(c, legacyRefreshCookiePath)
}

func randomValue() (string, error) {
	data := make([]byte, 32)

	if _, err := rand.Read(data); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (h *OauthHandler) Login(c *gin.Context) {
	state, err := randomValue()
	if err != nil {
		c.JSON(500, gin.H{"error": "生成 state 失败"})
		return
	}

	nonce, err := randomValue()
	if err != nil {
		c.JSON(500, gin.H{"error": "生成 nonce 失败"})
		return
	}

	// PKCE code_verifier。
	verifier := oauth2.GenerateVerifier()

	if err := h.service.StoreFlow(state, oidcCustom.LoginFlow{
		Nonce:     nonce,
		Verifier:  verifier,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}, c.Request.Context()); err != nil {
		c.JSON(500, gin.H{"error": "保存登录状态失败"})
		return
	}

	loginURL := h.service.AuthCodeURL(
		state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("prompt", "select_account consent"),
		oauth2.SetAuthURLParam("access_type", "offline"),
	)

	c.Redirect(302, loginURL)
}

type IDTokenClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func (h *OauthHandler) Callback(c *gin.Context) {
	ctx := c.Request.Context()
	state := c.Query("state")

	// 读取后立即删除，确保 state 只能使用一次。
	flow, exists := h.service.PopFlow(state, ctx)

	if !exists || time.Now().After(flow.ExpiresAt) {
		h.redirectToFrontend(c, "error", "invalid_state")
		return
	}

	if providerError := c.Query("error"); providerError != "" {
		h.redirectToFrontend(c, "error", "provider_denied")
		return
	}

	code := c.Query("code")
	if code == "" {
		h.redirectToFrontend(c, "error", "missing_code")
		return
	}

	// 使用授权码和 PKCE verifier 换取 Token。
	oauthToken, err := h.service.Exchange(
		ctx,
		code,
		oauth2.VerifierOption(flow.Verifier),
	)
	if err != nil {
		h.redirectToFrontend(c, "error", "exchange_failed")
		return
	}

	// OIDC 的 ID Token 位于 OAuth2 Token 的附加字段中。
	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok {
		h.redirectToFrontend(c, "error", "missing_id_token")
		return
	}

	// 验证签名、issuer、audience、过期时间等。
	idToken, err := h.service.VerifyIDToken(ctx, rawIDToken)
	if err != nil {
		h.redirectToFrontend(c, "error", "invalid_id_token")
		return
	}

	// go-oidc 不会自动比较 nonce，需要自己验证。
	if !secureEqual(idToken.Nonce, flow.Nonce) {
		h.redirectToFrontend(c, "error", "invalid_nonce")
		return
	}

	var claims IDTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		h.redirectToFrontend(c, "error", "invalid_claims")
		return
	}

	// 如果业务依赖邮箱，应要求 email_verified=true。
	if claims.Email != "" && !claims.EmailVerified {
		h.redirectToFrontend(c, "error", "email_not_verified")
		return
	}

	userID, err := h.service.FindOrCreateUser(
		ctx,
		idToken,
		claims,
	)
	if err != nil {
		h.redirectToFrontend(c, "error", "local_user_failed")
		return
	}

	err = h.service.AddToken(ctx, oauthToken, userID, idToken)
	if err != nil {
		h.redirectToFrontend(c, "error", "add_token_failed")
		return
	}

	_, refreshToken, err := h.authservice.LoginOIDC(ctx, userID)
	switch {
	case err == nil:
		h.setRefreshCookie(c, refreshToken)
		h.redirectToFrontend(c, "success", "")
	case errors.Is(err, auth.ErrUserDisabled):
		h.redirectToFrontend(c, "error", "user_disabled")
	case errors.Is(err, auth.ErrUserNotFound):
		h.redirectToFrontend(c, "error", "user_not_found")
	default:
		h.redirectToFrontend(c, "error", "session_failed")
	}
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
