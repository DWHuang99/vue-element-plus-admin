package oidc

import (
	"context"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type LoginFlow struct {
	Nonce     string
	Verifier  string
	ExpiresAt time.Time
}

type OIDCAuth struct {
	OauthConfig *oauth2.Config
	Verifier    *oidc.IDTokenVerifier
}

func NewOIDCAuth(
	ctx context.Context,
	issuer string,
	clientID string,
	clientSecret string,
	redirectURL string,
) (*OIDCAuth, error) {
	// 通过 issuer 的 /.well-known/openid-configuration
	// 自动获取 authorization、token、JWKS 等地址。
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}

	oauthConfig := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes: []string{
			oidc.ScopeOpenID,
			oidc.ScopeProfile,
			oidc.ScopeEmail,
		},
	}

	return &OIDCAuth{
		OauthConfig: oauthConfig,
		Verifier: provider.Verifier(&oidc.Config{
			ClientID: clientID,
		}),
	}, nil
}
