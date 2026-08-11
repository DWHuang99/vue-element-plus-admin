// Package platform hosts the authenticated Platform operations boundary
// (contracts/iam-application.md §Outbox delivery port): the only path that
// may requeue a blocked outbox event. It is deliberately HTTP-free — there
// is no public browser requeue API — and it never touches IAM sqlc, pgx, or
// producer tables: the same-transaction epoch bump, epoch_started_at =
// available_at, epoch-attempt reset, total-attempt preservation and the
// immutable iam_outbox_requeues evidence row are all owned by the IAM
// OutboxDeliveryPort adapter (T061). Blocked events have no waiver-to-purge
// path in this feature.
package platform

import (
	"context"
	"errors"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
)

// RequeuePort is the minimal delivery-port surface the boundary needs.
type RequeuePort interface {
	// RequeueBlockedOutbox requires a trusted recovery context minted by the
	// authenticated adapter; IAM rejects non-blocked events.
	RequeueBlockedOutbox(ctx context.Context, eventID string, trustedCtx iam.TrustedRecoveryContext, approvedReasonCode string, availableAt time.Time) (int64, error)
}

// RequeuePolicy is the authorized surface: only these principals may
// requeue, and only with these approved reason codes. Empty sets authorize
// nothing (safe default).
type RequeuePolicy struct {
	AuthorizedPrincipalIDs map[int64]bool
	ApprovedReasonCodes    map[string]bool
}

// Rejection sentinels. The third rejection — non-blocked events — is
// surfaced as iam.ErrOutboxNotBlocked from the IAM port itself.
var (
	ErrUnauthorizedOperator = errors.New("unauthorized platform operator")
	ErrUnapprovedReason     = errors.New("unapproved requeue reason")
)

// PlatformVerifier is the injected authenticated adapter: it verifies the
// active platform-operator session and binds the recovery identity
// (principal, authorization source, approval, request, correlation). It is
// the ONLY source of trusted context — arbitrary caller text never feeds IAM.
type PlatformVerifier func(ctx context.Context) (iam.TrustedRecoveryContext, error)

// RequeueBoundary exposes the only requeue path in the system. Operators
// authenticate first; every Requeue call must carry the minted identity.
type RequeueBoundary struct {
	outbox RequeuePort
	policy RequeuePolicy
}

// NewRequeueBoundary wires the boundary to the IAM delivery port.
func NewRequeueBoundary(outbox RequeuePort, policy RequeuePolicy) *RequeueBoundary {
	return &RequeueBoundary{outbox: outbox, policy: policy}
}

// OperatorIdentity binds the verified recovery identity. The trusted
// context is unexported: only Authenticate can mint an identity, so no
// caller can fabricate trusted context from arbitrary text.
type OperatorIdentity struct {
	trusted iam.TrustedRecoveryContext
}

func (id OperatorIdentity) principal() iam.TrustedRecoveryContext { return id.trusted }

// Authenticate verifies the operator session through the injected verifier
// and mints the only identity Requeue accepts. Unauthorized principals are
// rejected here, before any recovery capability exists.
func (b *RequeueBoundary) Authenticate(ctx context.Context, verify PlatformVerifier) (OperatorIdentity, error) {
	trusted, err := verify(ctx)
	if err != nil {
		return OperatorIdentity{}, err
	}
	if !b.policy.AuthorizedPrincipalIDs[trusted.PrincipalID] {
		return OperatorIdentity{}, ErrUnauthorizedOperator
	}
	return OperatorIdentity{trusted: trusted}, nil
}

// Requeue requeues a blocked event at a delayed available_at with an
// approved reason code. Unapproved reason codes are rejected before the
// port call; non-blocked events are rejected by IAM (iam.ErrOutboxNotBlocked).
// The same-transaction epoch/evidence semantics are the IAM adapter's.
func (b *RequeueBoundary) Requeue(ctx context.Context, id OperatorIdentity, eventID, approvedReasonCode string, availableAt time.Time) (int64, error) {
	if !b.policy.ApprovedReasonCodes[approvedReasonCode] {
		return 0, ErrUnapprovedReason
	}
	return b.outbox.RequeueBlockedOutbox(ctx, eventID, id.trusted, approvedReasonCode, availableAt)
}
