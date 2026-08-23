package gmailapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
	oidcCustom "vue-element-plus-admin/backend/internal/middleware/oidc"
	"vue-element-plus-admin/backend/internal/security"

	"golang.org/x/oauth2"
)

var (
	ErrGoogleNotConnected    = errors.New("Google integration is not connected")
	ErrGoogleReconnectNeeded = errors.New("Google authorization must be renewed")
)

type GoogleTokenRepository interface {
	GetToken(context.Context, int64) (*db.GetGoogleTokenByUserIDRow, error)
	AddToken(context.Context, db.AddGoogleTokenParams) error
}

type GmailService struct {
	repository    GoogleTokenRepository
	oidcAuth      *oidcCustom.OIDCAuth
	encryptionKey []byte
}

func NewGmailService(
	repository GoogleTokenRepository,
	oidcAuth *oidcCustom.OIDCAuth,
	encryptionKey []byte,
) *GmailService {
	return &GmailService{
		repository:    repository,
		oidcAuth:      oidcAuth,
		encryptionKey: append([]byte(nil), encryptionKey...),
	}
}

type GoogleToken struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

func (s *GmailService) GetToken(ctx context.Context, userID int64) (*GoogleToken, error) {
	row, err := s.getTokenRow(ctx, userID)
	if err != nil {
		return nil, err
	}

	if tokenNeedsRefresh(row) {
		newToken, err := s.RefreshGoogleToken(ctx, userID)
		if err != nil {
			return nil, err
		}
		return &GoogleToken{
			AccessToken:  newToken.AccessToken,
			RefreshToken: newToken.RefreshToken,
			Expiry:       newToken.Expiry,
		}, nil
	}

	token, err := buildOAuthToken(row, s.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt Google token: %w", err)
	}
	return &GoogleToken{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		Expiry:       token.Expiry,
	}, nil
}

func (s *GmailService) getTokenRow(
	ctx context.Context,
	userID int64,
) (*db.GetGoogleTokenByUserIDRow, error) {
	row, err := s.repository.GetToken(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGoogleNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("get Google token: %w", err)
	}
	if row == nil {
		return nil, ErrGoogleNotConnected
	}
	return row, nil
}

func decryptOptionalToken(key []byte, encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}
	decrypted, err := security.Decrypt(key, []byte(encrypted))
	if err != nil {
		return "", err
	}
	return string(decrypted), nil
}

func buildOAuthToken(
	row *db.GetGoogleTokenByUserIDRow,
	encryptionKey []byte,
) (*oauth2.Token, error) {
	accessToken, err := decryptOptionalToken(encryptionKey, row.AccessTokenEncrypted)
	if err != nil {
		return nil, err
	}
	refreshToken, err := decryptOptionalToken(encryptionKey, row.RefreshTokenEncrypted)
	if err != nil {
		return nil, err
	}

	return &oauth2.Token{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    row.TokenType,
		Expiry:       row.Expiry,
	}, nil
}

func (s *GmailService) RefreshGoogleToken(
	ctx context.Context,
	userID int64,
) (*oauth2.Token, error) {
	row, err := s.getTokenRow(ctx, userID)
	if err != nil {
		return nil, err
	}

	oldToken, err := buildOAuthToken(row, s.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt Google token: %w", err)
	}
	if oldToken.RefreshToken == "" {
		return nil, ErrGoogleReconnectNeeded
	}
	if s.oidcAuth == nil || s.oidcAuth.OauthConfig == nil {
		return nil, errors.New("Google OAuth configuration is unavailable")
	}

	// TokenSource only refreshes expired tokens. Mark the copy expired so this
	// method also refreshes tokens that are about to expire or received a 401.
	refreshCandidate := &oauth2.Token{
		RefreshToken: oldToken.RefreshToken,
		TokenType:    oldToken.TokenType,
		Expiry:       time.Now().Add(-time.Minute),
	}
	newToken, err := s.oidcAuth.OauthConfig.
		TokenSource(ctx, refreshCandidate).
		Token()
	if err != nil {
		return nil, fmt.Errorf("refresh Google access token: %w", err)
	}

	// Google generally omits the refresh token from refresh responses.
	if newToken.RefreshToken == "" {
		newToken.RefreshToken = oldToken.RefreshToken
	}
	if newToken.TokenType == "" {
		newToken.TokenType = oldToken.TokenType
	}

	accessEncrypted, err := security.Encrypt(s.encryptionKey, []byte(newToken.AccessToken))
	if err != nil {
		return nil, fmt.Errorf("encrypt refreshed Google access token: %w", err)
	}

	refreshEncrypted := ""
	if newToken.RefreshToken != oldToken.RefreshToken {
		encrypted, err := security.Encrypt(s.encryptionKey, []byte(newToken.RefreshToken))
		if err != nil {
			return nil, fmt.Errorf("encrypt refreshed Google refresh token: %w", err)
		}
		refreshEncrypted = string(encrypted)
	}

	err = s.repository.AddToken(ctx, db.AddGoogleTokenParams{
		UserID:                userID,
		ProviderSubject:       row.ProviderSubject,
		AccessTokenEncrypted:  string(accessEncrypted),
		RefreshTokenEncrypted: refreshEncrypted,
		TokenType:             newToken.TokenType,
		Expiry:                newToken.Expiry,
		Scopes:                s.oidcAuth.OauthConfig.Scopes,
	})
	if err != nil {
		return nil, fmt.Errorf("save refreshed Google token: %w", err)
	}

	return newToken, nil
}

func tokenNeedsRefresh(token *db.GetGoogleTokenByUserIDRow) bool {
	return token.AccessTokenEncrypted == "" ||
		token.Expiry.IsZero() ||
		time.Now().Add(2*time.Minute).After(token.Expiry)
}
