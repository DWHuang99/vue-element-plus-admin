package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	db "vue-element-plus-admin/backend/internal/database/iam/generated"
	oidcCustom "vue-element-plus-admin/backend/internal/middleware/oidc"
	rdb "vue-element-plus-admin/backend/internal/middleware/redis"
	"vue-element-plus-admin/backend/internal/modules/user"
	"vue-element-plus-admin/backend/internal/security"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"
)

const defaultRegistrationRoleCode = "user"

var ErrInvalidExternalIdentity = errors.New("invalid external identity")

type ExternalUserService struct {
	database       *sql.DB
	repository     *GoogleUserRepository
	userRepository *user.UserRepository
	oidcAuth       *oidcCustom.OIDCAuth
	redisClient    *redis.Client
}

func NewExternalUserService(
	database *sql.DB,
	repository *GoogleUserRepository,
	userRepository *user.UserRepository,
	oidcAuth *oidcCustom.OIDCAuth,
	redisClient *redis.Client,
) *ExternalUserService {
	return &ExternalUserService{
		database:       database,
		repository:     repository,
		userRepository: userRepository,
		oidcAuth:       oidcAuth,
		redisClient:    redisClient,
	}
}

// StoreFlow 保存一次登录流程的状态，供 Callback 阶段校验。
func (s *ExternalUserService) StoreFlow(state string, flow oidcCustom.LoginFlow, ctx context.Context) error {
	data, err := json.Marshal(flow)
	if err != nil {
		return err
	}
	return rdb.SetState(s.redisClient, ctx, state, data, 5*time.Minute)
}

// PopFlow 读取并删除对应 state 的流程数据，确保 state 只能使用一次。
func (s *ExternalUserService) PopFlow(state string, ctx context.Context) (oidcCustom.LoginFlow, bool) {
	data, err := rdb.DeleteState(s.redisClient, ctx, state) // 删除 Redis 中的 state
	if err != nil {
		return oidcCustom.LoginFlow{}, false
	}
	var flow oidcCustom.LoginFlow
	if err := json.Unmarshal([]byte(data), &flow); err == nil {
		return flow, true
	}
	return oidcCustom.LoginFlow{}, false
}

// AuthCodeURL 生成授权跳转地址。
func (s *ExternalUserService) AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string {
	return s.oidcAuth.OauthConfig.AuthCodeURL(state, opts...)
}

// Exchange 使用授权码兑换 OAuth2 Token。
func (s *ExternalUserService) Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	return s.oidcAuth.OauthConfig.Exchange(ctx, code, opts...)
}

// VerifyIDToken 校验 ID Token 的签名、issuer、audience、过期时间等。
func (s *ExternalUserService) VerifyIDToken(ctx context.Context, rawIDToken string) (*oidc.IDToken, error) {
	return s.oidcAuth.Verifier.Verify(ctx, rawIDToken)
}

func (s *ExternalUserService) FindOrCreateUser(
	ctx context.Context,
	idToken *oidc.IDToken,
	claims IDTokenClaims,
) (int64, error) {
	if idToken == nil || idToken.Issuer == "" || idToken.Subject == "" {
		return 0, ErrInvalidExternalIdentity
	}

	identity := db.FindExternalUserParams{
		ProviderIssuer:  idToken.Issuer,
		ProviderSubject: idToken.Subject,
	}
	userID, err := s.repository.GetExternalUserID(ctx, identity)
	if err != nil {
		return 0, err
	}
	if userID > 0 {
		return userID, nil
	}

	passwordHash, err := externalAccountPasswordHash()
	if err != nil {
		return 0, err
	}
	userID, err = s.createExternalUserAccount(ctx, idToken, claims, passwordHash)
	if err == nil {
		return userID, nil
	}

	// 两个相同身份并发首次登录时，唯一约束只允许一个请求创建成功。
	// 失败的一方重新查询已创建的绑定，避免产生重复本地用户。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if existingUserID, findErr := s.repository.GetExternalUserID(ctx, identity); findErr == nil && existingUserID > 0 {
			return existingUserID, nil
		}
	}

	return 0, err
}

func (s *ExternalUserService) createExternalUserAccount(
	ctx context.Context,
	idToken *oidc.IDToken,
	claims IDTokenClaims,
	passwordHash string,
) (int64, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	userRepository := s.userRepository.WithTx(tx)
	externalUserRepository := s.repository.WithTx(tx)

	createdUser, err := userRepository.AddUser(
		ctx,
		externalUsername(claims.Name, idToken.Issuer, idToken.Subject),
		passwordHash,
		defaultRegistrationRoleCode,
	)
	if err != nil {
		return 0, err
	}

	err = externalUserRepository.CreateExternalUser(ctx, db.CreateExternalUserParams{
		UserID:          createdUser.ID,
		ProviderIssuer:  idToken.Issuer,
		ProviderSubject: idToken.Subject,
		Email:           claims.Email,
	})
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return createdUser.ID, nil
}

func externalUsername(displayName, issuer, subject string) string {
	const (
		maxUsernameRunes = 50
		hashLength       = 12
	)

	var normalized strings.Builder
	lastSeparator := false
	for _, current := range strings.TrimSpace(displayName) {
		switch {
		case unicode.IsLetter(current), unicode.IsNumber(current), current == '-', current == '_':
			normalized.WriteRune(current)
			lastSeparator = false
		case !lastSeparator:
			normalized.WriteRune('_')
			lastSeparator = true
		}
	}

	prefix := strings.Trim(normalized.String(), "_-")
	if prefix == "" {
		prefix = "oidc"
	}

	digest := sha256.Sum256([]byte(issuer + "\x00" + subject))
	suffix := "_" + hex.EncodeToString(digest[:])[:hashLength]
	maxPrefixRunes := maxUsernameRunes - len([]rune(suffix))
	prefixRunes := []rune(prefix)
	if len(prefixRunes) > maxPrefixRunes {
		prefix = string(prefixRunes[:maxPrefixRunes])
	}

	return prefix + suffix
}

func externalAccountPasswordHash() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate external account password: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(randomBytes)
	passwordHash, err := security.Hash(password)
	if err != nil {
		return "", fmt.Errorf("hash external account password: %w", err)
	}
	return passwordHash, nil
}
