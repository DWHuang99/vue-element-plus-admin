// T073: the minimal actor context builders (research.md Decision 12) — one
// source of truth for correlation/actor/idempotency propagation across every
// module port call. The correlation ID is always the middleware-validated
// request ID (never the token), the actor is the authenticated principal (0
// pre-principal), and OperationID is left empty for the BFF workflow service
// to mint.
package http

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
)

func newTestContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

func TestRequestContext_CarriesCorrelationActorAndIdempotency(t *testing.T) {
	c := newTestContext()
	c.Set(middleware.RequestIDHeader, "req-1.abc:def")
	c.Set(ContextPrincipal, iam.Principal{UserID: 42, Username: "admin"})
	c.Set(ContextIdempotencyKey, "key-1234567890abcd")

	ctx := requestContext(c)
	assert.Equal(t, "req-1.abc:def", ctx.CorrelationID, "correlation = validated request ID")
	assert.Equal(t, int64(42), ctx.ActorUserID, "actor = authenticated principal")
	require.NotNil(t, ctx.IdempotencyKey)
	assert.Equal(t, "key-1234567890abcd", *ctx.IdempotencyKey)
	assert.Empty(t, ctx.OperationID, "BFF workflow service mints the authoritative operation ID")
}

func TestRequestContext_PrePrincipalActorZero(t *testing.T) {
	c := newTestContext()
	c.Set(middleware.RequestIDHeader, "req-1")

	ctx := requestContext(c)
	assert.Equal(t, "req-1", ctx.CorrelationID)
	assert.Zero(t, ctx.ActorUserID, "auth operations run pre-principal")
	assert.Nil(t, ctx.IdempotencyKey, "no Idempotency-Key middleware on auth routes")
}

func TestIamRequestContext_SamePropagationShapedForIAM(t *testing.T) {
	c := newTestContext()
	c.Set(middleware.RequestIDHeader, "req-2.xyz")
	c.Set(ContextPrincipal, iam.Principal{UserID: 7})

	ctx := iamRequestContext(c)
	assert.Equal(t, "req-2.xyz", ctx.CorrelationID)
	assert.Equal(t, int64(7), ctx.ActorUserID)
}

func TestOrgRequestContext_SamePropagationShapedForOrg(t *testing.T) {
	c := newTestContext()
	c.Set(middleware.RequestIDHeader, "req-3")
	c.Set(ContextPrincipal, iam.Principal{UserID: 9})
	c.Set(ContextIdempotencyKey, "dept-key-12345678")

	ctx := orgRequestContext(c)
	assert.Equal(t, "req-3", ctx.CorrelationID)
	assert.Equal(t, int64(9), ctx.ActorUserID)
	require.NotNil(t, ctx.IdempotencyKey)
	assert.Equal(t, "dept-key-12345678", *ctx.IdempotencyKey)
}

// The builders never touch request bodies, so context.Background is fine —
// this asserts the helper signature is safe for any handler wiring.
func TestRequestContext_DoesNotDependOnHandlerState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newTestContext()
	c.Set(middleware.RequestIDHeader, "req-4")
	c.Request = httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	op := requestContext(c)
	assert.Equal(t, "req-4", op.CorrelationID)
}
