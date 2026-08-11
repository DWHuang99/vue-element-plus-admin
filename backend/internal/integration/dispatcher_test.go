package integration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// Contract-first dispatcher suite (contracts/domain-events.md §Outbox delivery
// state rules 1-10) over fake ports — no database. The real outbox adapter
// semantics are fixed by the T061 integration suite; here the loop and its
// ack/failure/block classification are pinned.

func testDispatcher(outbox iam.OutboxService, inbox organization.InboxConsumer) *Dispatcher {
	cfg := DefaultDispatcherConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.CallTimeout = 500 * time.Millisecond
	cfg.BackoffBase = 5 * time.Second
	cfg.BackoffMax = 10 * time.Second
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewDispatcher(outbox, inbox, logger, cfg)
}

// --- fakes -------------------------------------------------------------------

type ackCall struct {
	eventID    string
	claimToken string
}

type failureCall struct {
	eventID    string
	claimToken string
	code       string
	next       time.Time
}

type blockCall struct {
	eventID    string
	claimToken string
	reason     string
}

type fakeOutbox struct {
	mu         sync.Mutex
	claimQueue [][]iam.ClaimedEvent
	claimErr   error
	claimCalls int

	acked      []ackCall
	failures   []failureCall
	blocks     []blockCall
	ackErr     error
	failureErr error
	blockErr   error
}

func (f *fakeOutbox) ClaimOutbox(_ context.Context, _ int, _ string, _ time.Duration) ([]iam.ClaimedEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if f.claimErr != nil {
		err := f.claimErr
		f.claimErr = nil // one-shot failure injection
		return nil, err
	}
	if len(f.claimQueue) == 0 {
		return nil, nil
	}
	batch := f.claimQueue[0]
	f.claimQueue = f.claimQueue[1:]
	return batch, nil
}

func (f *fakeOutbox) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimCalls
}

func (f *fakeOutbox) ackedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acked)
}

func (f *fakeOutbox) AckOutbox(_ context.Context, eventID, claimToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acked = append(f.acked, ackCall{eventID: eventID, claimToken: claimToken})
	return nil
}

func (f *fakeOutbox) RecordOutboxFailure(_ context.Context, eventID, claimToken, safeErrorCode string, nextAvailableAt time.Time) (iam.OutboxDeliveryStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failureErr != nil {
		return "", f.failureErr
	}
	f.failures = append(f.failures, failureCall{eventID: eventID, claimToken: claimToken, code: safeErrorCode, next: nextAvailableAt})
	return iam.OutboxPending, nil
}

func (f *fakeOutbox) BlockOutbox(_ context.Context, eventID, claimToken, safeReasonCode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blockErr != nil {
		return f.blockErr
	}
	f.blocks = append(f.blocks, blockCall{eventID: eventID, claimToken: claimToken, reason: safeReasonCode})
	return nil
}

func (f *fakeOutbox) RequeueBlockedOutbox(context.Context, string, iam.TrustedRecoveryContext, string, time.Time) (int64, error) {
	panic("dispatcher must never requeue")
}

func (f *fakeOutbox) GetOutboxBacklog(context.Context) (iam.OutboxBacklog, error) {
	panic("dispatcher must never read backlog")
}

type fakeInbox struct {
	mu      sync.Mutex
	events  []organization.IAMUserDeletedEvent
	err     error
	started chan struct{} // closed when a handler call begins
	blocked chan struct{} // if non-nil, handler blocks until closed
}

func (f *fakeInbox) HandleIAMUserDeletedV1(_ context.Context, event organization.IAMUserDeletedEvent) error {
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
	if f.started != nil {
		select {
		case <-f.started:
		default:
			close(f.started)
		}
	}
	if f.blocked != nil {
		<-f.blocked
	}
	return f.err
}

func (f *fakeInbox) got() []organization.IAMUserDeletedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]organization.IAMUserDeletedEvent(nil), f.events...)
}

// --- Dispatch: success -------------------------------------------------------

func TestDispatch_SuccessAcksWithClaimTokenCAS(t *testing.T) {
	outbox := &fakeOutbox{}
	inbox := &fakeInbox{}
	d := testDispatcher(outbox, inbox)

	ev := contractClaimed()
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	want := organization.IAMUserDeletedEvent{
		EventID:          ev.EventID,
		EventType:        ev.EventType,
		EventVersion:     ev.EventVersion,
		Producer:         ev.Producer,
		AggregateType:    ev.AggregateType,
		AggregateID:      ev.AggregateID,
		AggregateVersion: ev.AggregateVersion,
		UserID:           42,
	}
	got := inbox.got()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("inbox events mismatch: got %+v want %+v", got, want)
	}
	if len(outbox.acked) != 1 || outbox.acked[0] != (ackCall{eventID: ev.EventID, claimToken: ev.ClaimToken}) {
		t.Fatalf("ack mismatch: got %+v", outbox.acked)
	}
	if len(outbox.failures) != 0 || len(outbox.blocks) != 0 {
		t.Fatalf("unexpected failure/block calls: %+v %+v", outbox.failures, outbox.blocks)
	}
}

// --- Dispatch: decode/validate classification --------------------------------

func TestDispatch_UnsupportedVersionBlocks(t *testing.T) {
	outbox := &fakeOutbox{}
	inbox := &fakeInbox{}
	d := testDispatcher(outbox, inbox)

	ev := contractClaimed()
	ev.EventVersion = 2
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(outbox.blocks) != 1 || outbox.blocks[0].reason != "unsupported_version" {
		t.Fatalf("block mismatch: got %+v", outbox.blocks)
	}
	if len(inbox.got()) != 0 {
		t.Fatalf("consumer must not be called for unsupported version")
	}
	if len(outbox.acked) != 0 {
		t.Fatalf("unsupported version must not ack")
	}
}

func TestDispatch_ContractMismatchBlocks(t *testing.T) {
	for name, mut := range map[string]func(*iam.ClaimedEvent){
		"producer":       func(e *iam.ClaimedEvent) { e.Producer = "rbac" },
		"event type":     func(e *iam.ClaimedEvent) { e.EventType = "iam.user.renamed" },
		"aggregate type": func(e *iam.ClaimedEvent) { e.AggregateType = "group" },
		"aggregate id":   func(e *iam.ClaimedEvent) { e.AggregateID = "43" },
		"extra payload":  func(e *iam.ClaimedEvent) { e.Payload = map[string]any{"user_id": float64(42), "email": "x@y"} },
		"user id zero":   func(e *iam.ClaimedEvent) { e.Payload = map[string]any{"user_id": float64(0)} },
	} {
		t.Run(name, func(t *testing.T) {
			outbox := &fakeOutbox{}
			d := testDispatcher(outbox, &fakeInbox{})
			ev := contractClaimed()
			mut(&ev)
			if err := d.Dispatch(context.Background(), ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(outbox.blocks) != 1 || outbox.blocks[0].reason != "contract_mismatch" {
				t.Fatalf("block mismatch: got %+v", outbox.blocks)
			}
		})
	}
}

func TestDispatch_MalformedEnvelopeBlocks(t *testing.T) {
	for name, mut := range map[string]func(*iam.ClaimedEvent){
		"bad correlation": func(e *iam.ClaimedEvent) { e.CorrelationID = "has space" },
		"bad event id":    func(e *iam.ClaimedEvent) { e.EventID = "not-a-uuid" },
		"non-utc time": func(e *iam.ClaimedEvent) {
			e.OccurredAt = time.Date(2026, 8, 10, 12, 0, 0, 0, time.FixedZone("+08", 8*3600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			outbox := &fakeOutbox{}
			d := testDispatcher(outbox, &fakeInbox{})
			ev := contractClaimed()
			mut(&ev)
			if err := d.Dispatch(context.Background(), ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(outbox.blocks) != 1 || outbox.blocks[0].reason != "malformed_envelope" {
				t.Fatalf("block mismatch: got %+v", outbox.blocks)
			}
			if len(outbox.acked) != 0 {
				t.Fatalf("malformed envelope must not ack")
			}
		})
	}
}

// --- Dispatch: consumer failure classification --------------------------------

func TestDispatch_ConsumerUnavailableRecordsFailure(t *testing.T) {
	outbox := &fakeOutbox{}
	inbox := &fakeInbox{err: organization.ErrUnavailable}
	d := testDispatcher(outbox, inbox)

	ev := contractClaimed()
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(outbox.failures) != 1 {
		t.Fatalf("want one failure, got %+v", outbox.failures)
	}
	f := outbox.failures[0]
	if f.eventID != ev.EventID || f.claimToken != ev.ClaimToken || f.code != "consumer_unavailable" {
		t.Fatalf("failure mismatch: %+v", f)
	}
	// bounded backoff for attempt 1 == base
	if d := time.Until(f.next); d < 4*time.Second || d > 6*time.Second {
		t.Fatalf("backoff out of range: %v", d)
	}
	if len(outbox.acked) != 0 {
		t.Fatalf("transient failure must not ack")
	}
}

func TestDispatch_ConsumerTimeoutRecordsFailure(t *testing.T) {
	outbox := &fakeOutbox{}
	d := testDispatcher(outbox, &fakeInbox{err: organization.ErrTimeout})
	ev := contractClaimed()
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(outbox.failures) != 1 || outbox.failures[0].code != "consumer_timeout" {
		t.Fatalf("failure mismatch: %+v", outbox.failures)
	}
}

func TestDispatch_ConsumerInternalErrorIsTransient(t *testing.T) {
	outbox := &fakeOutbox{}
	d := testDispatcher(outbox, &fakeInbox{err: errors.New("db connection reset")})
	ev := contractClaimed()
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(outbox.failures) != 1 || outbox.failures[0].code != "consumer_internal" {
		t.Fatalf("failure mismatch: %+v", outbox.failures)
	}
}

func TestDispatch_ConsumerContractRejectionBlocks(t *testing.T) {
	// Defensive: the dispatcher already validated the envelope; a consumer
	// contract rejection is a durable block, never a hot retry.
	for name, err := range map[string]error{
		"invalid input":       organization.ErrInvalidInput,
		"unsupported version": organization.ErrUnsupportedEventVersion,
	} {
		t.Run(name, func(t *testing.T) {
			outbox := &fakeOutbox{}
			d := testDispatcher(outbox, &fakeInbox{err: err})
			ev := contractClaimed()
			if err := d.Dispatch(context.Background(), ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(outbox.blocks) != 1 || outbox.blocks[0].reason != "consumer_rejected_contract" {
				t.Fatalf("block mismatch: got %+v", outbox.blocks)
			}
			if len(outbox.failures) != 0 {
				t.Fatalf("contract rejection must not record transient failure: %+v", outbox.failures)
			}
		})
	}
}

func TestDispatch_BackoffScalesWithAttempt(t *testing.T) {
	outbox := &fakeOutbox{}
	d := testDispatcher(outbox, &fakeInbox{err: organization.ErrUnavailable})
	ev := contractClaimed()
	ev.AttemptCount = 3 // third claim of the epoch
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// base(5s) * 2^(3-1) = 20s, capped at max(10s)
	if d := time.Until(outbox.failures[0].next); d < 9*time.Second || d > 11*time.Second {
		t.Fatalf("capped backoff out of range: %v", d)
	}
}

func TestDispatch_BlockFailureReturnsError(t *testing.T) {
	// A block that itself fails (DB down) must surface so the loop logs it;
	// the lease expires and delivery retries.
	outbox := &fakeOutbox{blockErr: errors.New("db down")}
	d := testDispatcher(outbox, &fakeInbox{})
	ev := contractClaimed()
	ev.EventVersion = 2
	if err := d.Dispatch(context.Background(), ev); err == nil {
		t.Fatalf("Dispatch must surface block failure")
	}
}

// --- T064: durable block + alert path ----------------------------------------

// alertDispatcher wires an AlertOnBlock callback and records its calls.
func alertDispatcher(outbox iam.OutboxService, inbox organization.InboxConsumer) (*Dispatcher, *[]blockAlert) {
	alerts := &[]blockAlert{}
	cfg := DefaultDispatcherConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.CallTimeout = 500 * time.Millisecond
	cfg.AlertOnBlock = func(eventID, eventType, reason string) {
		*alerts = append(*alerts, blockAlert{eventID: eventID, eventType: eventType, reason: reason})
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewDispatcher(outbox, inbox, logger, cfg), alerts
}

type blockAlert struct {
	eventID   string
	eventType string
	reason    string
}

func TestDispatch_BlockedAlertFired(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*iam.ClaimedEvent)
		reason string
	}{
		{"unsupported version", func(e *iam.ClaimedEvent) { e.EventVersion = 2 }, "unsupported_version"},
		{"contract mismatch", func(e *iam.ClaimedEvent) { e.Producer = "rbac" }, "contract_mismatch"},
		{"malformed envelope", func(e *iam.ClaimedEvent) { e.CorrelationID = "has space" }, "malformed_envelope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := &fakeOutbox{}
			inbox := &fakeInbox{}
			d, alerts := alertDispatcher(outbox, inbox)
			ev := contractClaimed()
			tc.mut(&ev)
			if err := d.Dispatch(context.Background(), ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(outbox.blocks) != 1 || outbox.blocks[0].reason != tc.reason {
				t.Fatalf("block mismatch: got %+v", outbox.blocks)
			}
			if len(*alerts) != 1 || (*alerts)[0] != (blockAlert{eventID: ev.EventID, eventType: ev.EventType, reason: tc.reason}) {
				t.Fatalf("alert mismatch: got %+v", *alerts)
			}
			if len(inbox.got()) != 0 {
				t.Fatalf("blocked event must not touch the consumer (no successful inbox row)")
			}
		})
	}
}

func TestDispatch_ConsumerRejectionAlsoAlerts(t *testing.T) {
	outbox := &fakeOutbox{}
	d, alerts := alertDispatcher(outbox, &fakeInbox{err: organization.ErrInvalidInput})
	ev := contractClaimed()
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(*alerts) != 1 || (*alerts)[0].reason != "consumer_rejected_contract" {
		t.Fatalf("alert mismatch: got %+v", *alerts)
	}
}

func TestDispatch_SuccessAndTransientDoNotAlert(t *testing.T) {
	for name, inbox := range map[string]*fakeInbox{
		"success":   {},
		"transient": {err: organization.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			outbox := &fakeOutbox{}
			d, alerts := alertDispatcher(outbox, inbox)
			if err := d.Dispatch(context.Background(), contractClaimed()); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(*alerts) != 0 {
				t.Fatalf("no alert expected, got %+v", *alerts)
			}
		})
	}
}

func TestDispatch_BlockFailureDoesNotAlert(t *testing.T) {
	// If the block itself fails the lease expires and delivery retries; the
	// alert must not fire on every failed retry attempt.
	outbox := &fakeOutbox{blockErr: errors.New("db down")}
	d, alerts := alertDispatcher(outbox, &fakeInbox{})
	ev := contractClaimed()
	ev.EventVersion = 2
	if err := d.Dispatch(context.Background(), ev); err == nil {
		t.Fatalf("Dispatch must surface block failure")
	}
	if len(*alerts) != 0 {
		t.Fatalf("alert must not fire when the block failed, got %+v", *alerts)
	}
}

func TestRun_BlockedEventIsTerminalNoHotLoop(t *testing.T) {
	// A blocked event is never reclaimed: after the durable block the loop
	// proceeds to empty polls instead of re-delivering the same event.
	ev := contractClaimed()
	ev.EventVersion = 2
	outbox := &fakeOutbox{claimQueue: [][]iam.ClaimedEvent{{ev}}}
	inbox := &fakeInbox{}
	d, alerts := alertDispatcher(outbox, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for outbox.calls() < 3 { // one batch + at least two empty polls
		select {
		case <-deadline:
			t.Fatalf("Run stalled before empty polls")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outbox.blocks) != 1 {
		t.Fatalf("blocked event must be blocked exactly once, got %+v", outbox.blocks)
	}
	if len(outbox.acked) != 0 {
		t.Fatalf("blocked event must not ack")
	}
	if len(inbox.got()) != 0 {
		t.Fatalf("blocked event must not reach the consumer")
	}
	if len(*alerts) != 1 {
		t.Fatalf("want exactly one alert, got %+v", *alerts)
	}
}

// --- Run: claim loop, shutdown, drain ----------------------------------------

func TestRun_ClaimsBatchesThenPolls(t *testing.T) {
	outbox := &fakeOutbox{claimQueue: [][]iam.ClaimedEvent{{contractClaimed()}, {contractClaimed()}}}
	inbox := &fakeInbox{}
	d := testDispatcher(outbox, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for outbox.calls() < 3 {
		select {
		case <-deadline:
			t.Fatalf("Run did not poll again after draining backlog")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(inbox.got()); got != 2 {
		t.Fatalf("want 2 consumed events, got %d", got)
	}
	if got := len(outbox.acked); got != 2 {
		t.Fatalf("want 2 acks, got %d", got)
	}
}

func TestRun_StopsClaimingOnCancel(t *testing.T) {
	outbox := &fakeOutbox{}
	d := testDispatcher(outbox, &fakeInbox{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(20 * time.Millisecond) // let it enter the poll loop
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	callsAtExit := outbox.calls()
	time.Sleep(20 * time.Millisecond)
	if outbox.calls() != callsAtExit {
		t.Fatalf("Run kept claiming after shutdown: %d -> %d", callsAtExit, outbox.claimCalls)
	}
}

func TestRun_ClaimErrorRetries(t *testing.T) {
	outbox := &fakeOutbox{claimErr: errors.New("transient claim failure")}
	d := testDispatcher(outbox, &fakeInbox{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for outbox.calls() < 2 {
		select {
		case <-deadline:
			t.Fatalf("Run did not retry after claim error")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRun_DrainsInFlightBatchOnShutdown(t *testing.T) {
	ev1, ev2 := contractClaimed(), contractClaimed()
	ev2.EventID = "7c0e1d24-9b1a-4a3e-8c2f-5d6e7f8a9b0c"
	outbox := &fakeOutbox{claimQueue: [][]iam.ClaimedEvent{{ev1, ev2}}}
	inbox := &fakeInbox{started: make(chan struct{}), blocked: make(chan struct{})}
	d := testDispatcher(outbox, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	<-inbox.started      // ev1 handler is now executing
	cancel()             // shutdown while the batch is in flight
	close(inbox.blocked) // let ev1 finish

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The in-flight batch drained: ev2 was still delivered after shutdown.
	if got := len(inbox.got()); got != 2 {
		t.Fatalf("want both events drained, got %d", got)
	}
	if got := len(outbox.acked); got != 2 {
		t.Fatalf("want both acks after drain, got %d", got)
	}
}

// --- Contract: observability (T066) -----------------------------------------

// TestDispatch_LogsLatencyOnSuccess pins the dispatch observability log
// (contracts/domain-events.md §Required observability: dispatch attempt count
// and latency): every successful delivery records the attempt number and the
// per-call latency; the failure log carries it too.
func TestDispatch_LogsLatencyOnSuccess(t *testing.T) {
	ev := contractClaimed()
	ev.AttemptCount = 3
	outbox := &fakeOutbox{claimQueue: [][]iam.ClaimedEvent{{ev}}}
	inbox := &fakeInbox{}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := DefaultDispatcherConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.CallTimeout = 500 * time.Millisecond
	d := NewDispatcher(outbox, inbox, logger, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait for the delivery to land, then shut down.
	deadline := time.After(2 * time.Second)
	for outbox.ackedCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("delivery did not complete")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "dispatch_latency_ms=") {
		t.Fatalf("success log must carry dispatch_latency_ms, got: %s", logs)
	}
	if !strings.Contains(logs, "attempt=3") {
		t.Fatalf("success log must carry the attempt count, got: %s", logs)
	}
}

// TestDispatcher_HealthyReflectsLoopLifecycle (T071): the readiness probe
// reads the loop-alive signal — error before Run, nil while running, error
// again after Run exits.
func TestDispatcher_HealthyReflectsLoopLifecycle(t *testing.T) {
	outbox := &fakeOutbox{claimQueue: [][]iam.ClaimedEvent{nil}}
	inbox := &fakeInbox{}
	d := testDispatcher(outbox, inbox)

	if d.Healthy() == nil {
		t.Fatal("Healthy must fail before Run starts")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait until the loop is actually claiming (running flag set).
	deadline := time.After(2 * time.Second)
	for d.Healthy() != nil {
		select {
		case <-deadline:
			t.Fatal("dispatcher never became healthy")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.Healthy() == nil {
		t.Fatal("Healthy must fail after Run exits")
	}
}
