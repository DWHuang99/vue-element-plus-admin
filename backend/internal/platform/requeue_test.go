package platform

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
)

// Contract-first requeue boundary suite (contracts/iam-application.md §Outbox
// delivery port: trusted_recovery_context is created only by an authenticated
// Platform operations adapter; IAM rejects arbitrary caller text, unauthorized
// principals, non-blocked events and unapproved reason codes; there is no
// public browser requeue API).

type requeueCall struct {
	eventID   string
	trusted   iam.TrustedRecoveryContext
	reason    string
	available time.Time
}

type fakeOutbox struct {
	requeues     []requeueCall
	requeueErr   error
	requeueEpoch int64
}

func (f *fakeOutbox) RequeueBlockedOutbox(_ context.Context, eventID string, trustedCtx iam.TrustedRecoveryContext, approvedReasonCode string, availableAt time.Time) (int64, error) {
	if f.requeueErr != nil {
		return 0, f.requeueErr
	}
	f.requeues = append(f.requeues, requeueCall{eventID: eventID, trusted: trustedCtx, reason: approvedReasonCode, available: availableAt})
	return f.requeueEpoch, nil
}

// testPolicy: principal 7 is authorized; "approved-bug-fix" is an approved reason.
func testPolicy() RequeuePolicy {
	return RequeuePolicy{
		AuthorizedPrincipalIDs: map[int64]bool{7: true},
		ApprovedReasonCodes:    map[string]bool{"approved-bug-fix": true},
	}
}

func testBoundary(outbox *fakeOutbox) *RequeueBoundary {
	return NewRequeueBoundary(outbox, testPolicy())
}

// verifier is the injected authenticated adapter: it verifies the operator
// session and binds the recovery identity. It is the ONLY source of the
// trusted context fields — arbitrary caller text never reaches Requeue.
func verifier(trusted iam.TrustedRecoveryContext, err error) PlatformVerifier {
	return func(context.Context) (iam.TrustedRecoveryContext, error) {
		return trusted, err
	}
}

var errSessionExpired = errors.New("operator session expired")

func TestRequeue_AuthorizedPathMintsAndRequeues(t *testing.T) {
	outbox := &fakeOutbox{requeueEpoch: 2}
	b := testBoundary(outbox)

	id, err := b.Authenticate(context.Background(), verifier(iam.TrustedRecoveryContext{
		PrincipalID:         7,
		AuthorizationSource: "platform-cli",
		ApprovalID:          "appr-123",
		RequestID:           "req-456",
		CorrelationID:       "corr-789",
	}, nil))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	at := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	epoch, err := b.Requeue(context.Background(), id, "3f9b6b8e-9e44-4f5b-9c1e-2a4d5e6f7a8b", "approved-bug-fix", at)
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if epoch != 2 {
		t.Fatalf("epoch = %d, want 2", epoch)
	}
	if len(outbox.requeues) != 1 {
		t.Fatalf("want 1 requeue, got %d", len(outbox.requeues))
	}
	call := outbox.requeues[0]
	if call.eventID != "3f9b6b8e-9e44-4f5b-9c1e-2a4d5e6f7a8b" || call.reason != "approved-bug-fix" || !call.available.Equal(at) {
		t.Fatalf("requeue call mismatch: %+v", call)
	}
	// The full trusted identity is bound and forwarded verbatim.
	if call.trusted != (iam.TrustedRecoveryContext{
		PrincipalID:         7,
		AuthorizationSource: "platform-cli",
		ApprovalID:          "appr-123",
		RequestID:           "req-456",
		CorrelationID:       "corr-789",
	}) {
		t.Fatalf("trusted context mismatch: %+v", call.trusted)
	}
}

func TestRequeue_UnauthorizedPrincipalRejected(t *testing.T) {
	outbox := &fakeOutbox{}
	b := testBoundary(outbox)

	// Principal 99 is not in the authorized set — rejected before minting.
	_, err := b.Authenticate(context.Background(), verifier(iam.TrustedRecoveryContext{
		PrincipalID:         99,
		AuthorizationSource: "platform-cli",
		ApprovalID:          "appr-123",
		RequestID:           "req-456",
		CorrelationID:       "corr-789",
	}, nil))
	if !errors.Is(err, ErrUnauthorizedOperator) {
		t.Fatalf("want ErrUnauthorizedOperator, got %v", err)
	}
	if len(outbox.requeues) != 0 {
		t.Fatalf("unauthorized principal must not reach the outbox")
	}
}

func TestRequeue_UnapprovedReasonRejected(t *testing.T) {
	outbox := &fakeOutbox{}
	b := testBoundary(outbox)
	id, err := b.Authenticate(context.Background(), verifier(iam.TrustedRecoveryContext{PrincipalID: 7}, nil))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	// "arbitrary caller text" as reason code is not on the approved list.
	_, err = b.Requeue(context.Background(), id, "event-1", "because-i-said-so", time.Now())
	if !errors.Is(err, ErrUnapprovedReason) {
		t.Fatalf("want ErrUnapprovedReason, got %v", err)
	}
	if len(outbox.requeues) != 0 {
		t.Fatalf("unapproved reason must not reach the outbox")
	}
}

func TestRequeue_VerificationFailureRejected(t *testing.T) {
	outbox := &fakeOutbox{}
	b := testBoundary(outbox)
	_, err := b.Authenticate(context.Background(), verifier(iam.TrustedRecoveryContext{}, errSessionExpired))
	if !errors.Is(err, errSessionExpired) {
		t.Fatalf("verification failure must surface, got %v", err)
	}
	if len(outbox.requeues) != 0 {
		t.Fatalf("failed verification must not reach the outbox")
	}
}

func TestRequeue_NonBlockedEventRejected(t *testing.T) {
	outbox := &fakeOutbox{requeueErr: iam.ErrOutboxNotBlocked}
	b := testBoundary(outbox)
	id, err := b.Authenticate(context.Background(), verifier(iam.TrustedRecoveryContext{PrincipalID: 7}, nil))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	_, err = b.Requeue(context.Background(), id, "event-1", "approved-bug-fix", time.Now())
	if !errors.Is(err, iam.ErrOutboxNotBlocked) {
		t.Fatalf("want iam.ErrOutboxNotBlocked, got %v", err)
	}
}

func TestRequeue_EmptyPolicyRejectsEverything(t *testing.T) {
	// Safe default: no principals authorized, no reasons approved.
	outbox := &fakeOutbox{}
	b := NewRequeueBoundary(outbox, RequeuePolicy{})
	if _, err := b.Authenticate(context.Background(), verifier(iam.TrustedRecoveryContext{PrincipalID: 1}, nil)); !errors.Is(err, ErrUnauthorizedOperator) {
		t.Fatalf("empty policy must reject every principal, got %v", err)
	}
	if len(outbox.requeues) != 0 {
		t.Fatalf("empty policy must not reach the outbox")
	}
}

func TestRequeue_IdentityCannotBeFabricated(t *testing.T) {
	// The identity's trusted fields are unexported — no caller can mint an
	// OperatorIdentity from arbitrary text; only Authenticate can. This test
	// documents the boundary contract (compile-time enforced by the package).
	outbox := &fakeOutbox{}
	b := testBoundary(outbox)
	id, err := b.Authenticate(context.Background(), verifier(iam.TrustedRecoveryContext{
		PrincipalID:         7,
		AuthorizationSource: "platform-cli",
		ApprovalID:          "appr-123",
		RequestID:           "req-456",
		CorrelationID:       "corr-789",
	}, nil))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	// The only way to obtain a trusted context is the authenticated adapter.
	if id.principal().PrincipalID != 7 || id.principal().ApprovalID != "appr-123" {
		t.Fatalf("minted identity mismatch: %+v", id.principal())
	}
}
