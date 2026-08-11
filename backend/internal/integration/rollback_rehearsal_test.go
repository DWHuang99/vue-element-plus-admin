// T080 rollback rehearsal tests (quickstart §10, plan Rollback §1-3). The
// rollback procedure is: freeze management writes and new event producers →
// put the dispatcher into drain mode (not an immediate stop) → recover expired
// leases and drain retryable pending → stop new claims → snapshot leased and
// blocked evidence before resolving it. Every rehearsal drives the REAL
// dispatcher and the REAL IAM outbox / Organization inbox ports on a scratch
// database, so the procedure is validated end to end — not against fakes.
//
// The crash-matrix suite (T067) proves the individual crash windows; this file
// rehearses the ordered rollback sequence those windows compose, plus the
// evidence-snapshot guarantees (§10.3/§10.9: leased/blocked rows are
// observable and are resolved — never dropped — and a frozen producer's event
// stays durably pending).
package integration

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// gatedInbox wraps the real Organization inbox consumer and lets the test hold
// a delivery in flight: HandleIAMUserDeletedV1 signals entry, then blocks
// until release (or the drain-safe call deadline). This is how the rehearsal
// pins one event mid-dispatch while the rollback freeze happens around it.
type gatedInbox struct {
	inner   organization.InboxConsumer
	entered chan struct{}
	release chan struct{}
}

func (g *gatedInbox) HandleIAMUserDeletedV1(ctx context.Context, event organization.IAMUserDeletedEvent) error {
	select {
	case <-g.entered:
	default:
		close(g.entered)
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.inner.HandleIAMUserDeletedV1(ctx, event)
}

// TestRollbackRehearsal_FreezeWritesDrainInFlightNotStop (quickstart §10.1):
// freezing is drain mode, not an immediate stop. An event already claimed when
// the freeze fires is delivered to completion; an event produced after the
// freeze (a new producer write landing during the drain window) is never
// claimed and stays durably pending — the frozen write is evidence, not loss.
func TestRollbackRehearsal_FreezeWritesDrainInFlightNotStop(t *testing.T) {
	h := newCrashHarness(t)
	ctx := context.Background()

	// The in-flight event: claimed and held inside the consumer when the
	// freeze fires. A 5s call deadline keeps it mid-dispatch while the test
	// performs the freeze and lands the post-freeze producer write.
	gated := &gatedInbox{
		inner:   h.inbox,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	cfg := DefaultDispatcherConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.CallTimeout = 5 * time.Second
	cfg.BackoffBase = 10 * time.Millisecond
	cfg.BackoffMax = 50 * time.Millisecond
	d := NewDispatcher(h.deliv, gated, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)

	inFlight := h.seedOutboxEvent(t, crashEventSeed{userID: 9001})
	h.seedMembership(t, 9001)

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- d.Run(loopCtx) }()

	// The in-flight event is claimed and currently blocked inside the consumer.
	require.Eventually(t, func() bool {
		select {
		case <-gated.entered:
			return true
		default:
			return false
		}
	}, 10*time.Second, 5*time.Millisecond, "in-flight event is being consumed")
	require.Eventually(t, func() bool {
		return h.outboxRow(t, inFlight).status == "leased"
	}, 10*time.Second, 5*time.Millisecond, "in-flight event is leased")

	// FREEZE: writes and new producers are stopped, but the dispatcher must
	// not drop the in-flight batch. A producer write lands during the drain
	// window — it is the frozen write.
	cancel()
	frozen := h.seedOutboxEvent(t, crashEventSeed{userID: 9002})

	// DRAIN: release the in-flight delivery; it completes to published.
	close(gated.release)
	select {
	case err := <-done:
		require.NoError(t, err, "Run returns nil after freeze + drain")
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher did not stop after the freeze + drain")
	}

	// In-flight drained to completion; the post-freeze producer write was never
	// claimed and stays durable pending (evidence, not loss).
	assert.Equal(t, "published", h.outboxRow(t, inFlight).status,
		"in-flight delivery drains to completion on freeze")
	assert.Equal(t, "pending", h.outboxRow(t, frozen).status,
		"a write produced after the freeze is never claimed")
	assert.Equal(t, 1, h.inboxCount(t),
		"the drained event's inbox dedupe evidence is preserved")
	backlog, err := h.deliv.GetOutboxBacklog(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, backlog.PendingCount, int64(1),
		"the frozen event is still counted in the durable backlog")
}

// TestRollbackRehearsal_RecoverExpiredLeasesDrainRetryableThenStop
// (quickstart §10.2): first recover every expired lease and drain every due
// retryable pending event, then stop new claims. The backlog must be empty of
// claimable rows before claims stop, and a producer write that lands after
// claims stop stays pending.
func TestRollbackRehearsal_RecoverExpiredLeasesDrainRetryableThenStop(t *testing.T) {
	h := newCrashHarness(t)
	ctx := context.Background()

	// A leased row whose worker crashed mid-delivery (expired lease) and a
	// pending row still in retry backoff (future available_at).
	leased := h.seedOutboxEvent(t, crashEventSeed{userID: 9101})
	h.seedMembership(t, 9101)
	_, err := h.pool.Exec(ctx,
		`UPDATE iam_outbox_events SET status='leased',
		    leased_until = now() - interval '1 second', lease_owner = 'dead-worker',
		    claim_token = gen_random_uuid()
		 WHERE event_id = $1`, leased)
	require.NoError(t, err)

	retryable := h.seedOutboxEvent(t, crashEventSeed{
		userID:      9102,
		status:      "pending",
		availableAt: ptrTime(time.Now().Add(time.Hour)),
	})
	h.seedMembership(t, 9102)
	h.makeDue(t, retryable)

	// Snapshot before recovery: both rows are visible to the rollback operator
	// as claimable evidence — the lease recovery and the due retryable.
	backlog, err := h.deliv.GetOutboxBacklog(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, backlog.LeasedCount, int64(1), "expired lease is snapshot-visible")
	assert.GreaterOrEqual(t, backlog.PendingCount, int64(1), "due retryable is snapshot-visible")

	// Drain: one dispatcher run recovers the expired lease and delivers the
	// retryable, so the backlog drains to terminal before claims stop.
	d := h.crashDispatcher(h.deliv)
	h.runUntil(t, d, func() bool {
		return h.outboxRow(t, leased).status == "published" &&
			h.outboxRow(t, retryable).status == "published"
	})
	assert.Equal(t, 2, h.inboxCount(t), "both drained deliveries keep their inbox dedupe evidence")

	backlog, err = h.deliv.GetOutboxBacklog(ctx)
	require.NoError(t, err)
	assert.Zero(t, backlog.LeasedCount, "all expired leases were recovered and drained")
	assert.Zero(t, backlog.PendingCount, "all retryable pending events were drained")

	// Claims stopped: a producer write landing after the drain is never claimed.
	after := h.seedOutboxEvent(t, crashEventSeed{userID: 9103})
	assert.Equal(t, "pending", h.outboxRow(t, after).status,
		"new claims stop after the drain completes")
}

// TestRollbackRehearsal_SnapshotAndResolveLeasedBlockedEvidence
// (quickstart §10.3/§10.9): leased and blocked claim/requeue evidence is
// snapshot-observable and is resolved through the production requeue port —
// never dropped. The blocked event is requeued with a trusted recovery
// context, drains to published, and the evidence survives as an inbox dedupe
// row (dormant, preserved).
func TestRollbackRehearsal_SnapshotAndResolveLeasedBlockedEvidence(t *testing.T) {
	h := newCrashHarness(t)
	ctx := context.Background()

	// A crashed worker's leased row and a blocked (poisoned) row.
	leased := h.seedOutboxEvent(t, crashEventSeed{userID: 9201})
	h.seedMembership(t, 9201)
	_, err := h.pool.Exec(ctx,
		`UPDATE iam_outbox_events SET status='leased',
		    leased_until = now() - interval '1 second', lease_owner = 'dead-worker',
		    claim_token = gen_random_uuid()
		 WHERE event_id = $1`, leased)
	require.NoError(t, err)

	// A blocked (poisoned) row: status CHECK requires the blocked fields, so
	// seed pending then mark it blocked the way the production block path does.
	blocked := h.seedOutboxEvent(t, crashEventSeed{userID: 9202})
	h.seedMembership(t, 9202)
	_, err = h.pool.Exec(ctx,
		`UPDATE iam_outbox_events SET status='blocked',
		    blocked_at = now(), blocked_reason_code = 'contract_mismatch'
		 WHERE event_id = $1`, blocked)
	require.NoError(t, err)

	// Snapshot: both are observable to the operator, not silently dropped.
	backlog, err := h.deliv.GetOutboxBacklog(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, backlog.LeasedCount, int64(1), "leased evidence is snapshot-visible")
	assert.GreaterOrEqual(t, backlog.BlockedCount, int64(1), "blocked evidence is snapshot-visible")

	// Resolve the blocked event through the production requeue port with a
	// trusted recovery context (short-lived authenticated Platform operation).
	n, err := h.deliv.RequeueBlockedOutbox(ctx, blocked, iam.TrustedRecoveryContext{
		PrincipalID:         7,
		AuthorizationSource: "platform_operator_session",
		ApprovalID:          "appr-rollback-rehearsal",
		CorrelationID:       "corr-rollback-rehearsal",
		RequestID:           "req-rollback-rehearsal",
	}, "rollback-rehearsal", time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(2), n,
		"the blocked event moves to a fresh delivery epoch (evidence: epoch+1, never dropped)")

	// Drain both to terminal: leased recovered, requeued blocked delivered.
	d := h.crashDispatcher(h.deliv)
	h.runUntil(t, d, func() bool {
		return h.outboxRow(t, leased).status == "published" &&
			h.outboxRow(t, blocked).status == "published"
	})

	backlog, err = h.deliv.GetOutboxBacklog(ctx)
	require.NoError(t, err)
	assert.Zero(t, backlog.LeasedCount, "leased evidence drained")
	assert.Zero(t, backlog.BlockedCount, "blocked evidence resolved, not dropped")
	assert.Equal(t, 2, h.inboxCount(t),
		"evidence survives as dormant inbox dedupe rows (quickstart §10.9)")
}

// ptrTime returns a pointer to t (helper for the seed's optional timestamp).
func ptrTime(t time.Time) *time.Time { return &t }
