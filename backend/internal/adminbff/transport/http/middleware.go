// BFF HTTP middleware (T024): authentication through the IAM AuthService and
// per-route authorization through the IAM IdentityService permission matrix.
//
// Wire parity rules (contracts/http-api-compatibility.md):
//   - missing/malformed token and invalid session both surface as 401
//     AUTH_INVALID_TOKEN, with the same public messages as the legacy router;
//   - an authenticated principal without the required permission surfaces as
//     403 AUTH_FORBIDDEN — 403 never revokes the session and never touches
//     session state (distinct from 401, which the auth middleware owns);
//   - authorization errors (infrastructure) surface as 500 INTERNAL_ERROR.
//
// The permission constants are defined here — the BFF must not import the
// legacy authorization package.
package http

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// BFF permission codes (permission matrix, contracts/http-api-compatibility.md).
const (
	PermissionRolesRead        = "roles.read"
	PermissionRolesWrite       = "roles.write"
	PermissionDepartmentsRead  = "departments.read"
	PermissionDepartmentsWrite = "departments.write"
	PermissionUsersRead        = "users.read"
	PermissionUsersWrite       = "users.write"
)

// ContextPrincipal is the gin context key for the authenticated iam.Principal.
const ContextPrincipal = "adminbff.principal"

// ContextRawToken is the gin context key for the parsed raw Bearer token
// (format-only middleware; the session endpoints own the raw value exactly
// once — the principal/session stores only the hash).
const ContextRawToken = "adminbff.raw_token"

// ContextIdempotencyKey is the gin context key for the validated
// Idempotency-Key header (T056; contracts/http-api-compatibility.md
// Managed-user idempotency header).
const ContextIdempotencyKey = "adminbff.idempotency_key"

// idempotencyKeyRe is the public key charset/length: 16–128 ASCII
// [A-Za-z0-9._:-]+. The generated UUID form (36 chars) is a member.
var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{16,128}$`)

// principalFromContext returns the authenticated principal or false.
func principalFromContext(c *gin.Context) (iam.Principal, bool) {
	v, ok := c.Get(ContextPrincipal)
	if !ok {
		return iam.Principal{}, false
	}
	p, ok := v.(iam.Principal)
	return p, ok
}

// Authenticate validates the Bearer token through IAM and injects the
// iam.Principal. A failure is always 401 AUTH_INVALID_TOKEN — the session
// itself is never revoked here.
func Authenticate(svc iam.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "缺少或格式无效的认证令牌",
				},
			})
			return
		}

		principal, err := svc.Authenticate(c.Request.Context(), token, iam.OperationContext{
			CorrelationID: c.GetString(middleware.RequestIDHeader),
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "认证令牌无效、已过期或已撤销",
				},
			})
			return
		}

		c.Set(ContextPrincipal, principal)
		c.Next()
	}
}

// BearerTokenFormat validates only the header format (no session validation)
// and injects the raw token. Used by logout so revoking an already-invalid
// token stays idempotent, while a missing/malformed header still gets 401.
func BearerTokenFormat() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "缺少或格式无效的认证令牌",
				},
			})
			return
		}
		c.Set(ContextRawToken, token)
		c.Next()
	}
}

// RequirePermission allows the request only when the authenticated principal
// holds the permission in the current IAM state. Runs after Authenticate.
// 403 AUTH_FORBIDDEN never revokes the session.
func RequirePermission(svc iam.IdentityService, permissionCode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := principalFromContext(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_TOKEN",
					"message": "缺少或无效的认证用户",
				},
			})
			return
		}

		allowed, err := svc.HasPermission(c.Request.Context(), principal.UserID, permissionCode)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"code":    "INTERNAL_ERROR",
					"message": "内部错误",
				},
			})
			return
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"code":    "AUTH_FORBIDDEN",
					"message": "无权执行此操作",
				},
			})
			return
		}

		c.Next()
	}
}

// IdempotencyKey validates the optional Idempotency-Key header and injects
// the validated value (T056). The header is optional for backward
// compatibility — the official frontend sends it for every managed-user
// write — but any present value MUST match the contract format: an invalid
// value is 400 AUTH_INVALID_INPUT before any idempotency lookup. The header
// is never logged and never stored verbatim beyond the workflow's
// idempotency scope (the key itself is the safe representation).
func IdempotencyKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("Idempotency-Key")
		if key == "" {
			c.Next()
			return
		}
		if !idempotencyKeyRe.MatchString(key) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"code":    "AUTH_INVALID_INPUT",
					"message": "Idempotency-Key 需为 16-128 位字母、数字、. _ : - 字符",
				},
			})
			return
		}
		c.Set(ContextIdempotencyKey, key)
		c.Next()
	}
}

// idempotencyKeyFromContext returns the validated header value or nil.
func idempotencyKeyFromContext(c *gin.Context) *string {
	v, ok := c.Get(ContextIdempotencyKey)
	if !ok {
		return nil
	}
	key, ok := v.(string)
	if !ok || key == "" {
		return nil
	}
	return &key
}

// requestContext builds the BFF workflow context for the current request
// (T073, research.md Decision 12: minimal actor context — the raw token never
// crosses the transport boundary):
//   - CorrelationID is the middleware-validated request ID — it never comes
//     from the token and always fits the envelope v1 correlation charset, so
//     it can flow through participants into outbox events untouched;
//   - ActorUserID is the authenticated principal (0 for pre-principal auth
//     operations);
//   - IdempotencyKey is the validated header (nil when absent);
//   - OperationID is intentionally left empty — the BFF workflow service
//     mints the authoritative operation ID per attempt.
func requestContext(c *gin.Context) adminbff.OperationContext {
	return adminbff.OperationContext{
		IdempotencyKey: idempotencyKeyFromContext(c),
		ActorUserID:    actorUserID(c),
		CorrelationID:  c.GetString(middleware.RequestIDHeader),
	}
}

// iamRequestContext shapes the same minimal actor context for direct IAM
// port calls (auth operations run pre-principal, so the actor is 0).
func iamRequestContext(c *gin.Context) iam.OperationContext {
	ctx := requestContext(c)
	return iam.OperationContext{
		IdempotencyKey: ctx.IdempotencyKey,
		ActorUserID:    ctx.ActorUserID,
		CorrelationID:  ctx.CorrelationID,
	}
}

// orgRequestContext shapes the same minimal actor context for the
// Organization port (research.md Decision 12: Organization trusts the
// in-process BFF call boundary — no reverse per-operation IAM calls).
func orgRequestContext(c *gin.Context) organization.OperationContext {
	ctx := requestContext(c)
	return organization.OperationContext{
		IdempotencyKey: ctx.IdempotencyKey,
		ActorUserID:    ctx.ActorUserID,
		CorrelationID:  ctx.CorrelationID,
	}
}

// actorUserID resolves the authenticated principal's stable user ID, or 0
// when the request has not been authenticated yet.
func actorUserID(c *gin.Context) int64 {
	if p, ok := principalFromContext(c); ok {
		return p.UserID
	}
	return 0
}

// bearerToken extracts the raw token from "Authorization: Bearer <token>".
func bearerToken(c *gin.Context) (string, bool) {
	header := c.GetHeader("Authorization")
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}
