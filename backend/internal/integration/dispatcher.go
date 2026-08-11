// dispatcher.go — T063 in-process delivery loop (contracts/domain-events.md
// §Outbox delivery state rules 1-10).
//
// Bounded claims come out of a SHORT IAM transaction already persisted as
// leased (claim == attempt, crash-after-claim burns budget); no producer
// transaction is ever held across the consumer call. Every event is
// decode/validated through the envelope v1 codec before the Organization
// port is invoked; success acks with the claim-token CAS, transient consumer
// failures record one atomic failure transition with bounded backoff, and
// unsupported/contract/malformed envelopes block durably (never hot-loop).
// Shutdown stops new claims; the in-flight batch drains and each consumer
// call is bounded by its own deadline.
package integration

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// DispatcherConfig tunes the delivery loop.
type DispatcherConfig struct {
	LeaseOwner    string        // stable identity written into every lease
	BatchSize     int           // bounded claim batch
	LeaseDuration time.Duration // lease handed out per claim
	PollInterval  time.Duration // idle poll between empty claims
	CallTimeout   time.Duration // per consumer-call deadline (drain bound)
	BackoffBase   time.Duration // first transient failure backoff
	BackoffMax    time.Duration // cap for the exponential backoff
	// AlertOnBlock fires once per durable block with safe fields only
	// (event ID, type, reason — never payload). nil falls back to a Warn log.
	AlertOnBlock func(eventID, eventType, reason string)
}

// DefaultDispatcherConfig returns sane production defaults.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		LeaseOwner:    "outbox-dispatcher",
		BatchSize:     10,
		LeaseDuration: 30 * time.Second,
		PollInterval:  time.Second,
		CallTimeout:   30 * time.Second,
		BackoffBase:   5 * time.Second,
		BackoffMax:    15 * time.Minute,
	}
}

// Dispatcher drives the IAM outbox through the Organization inbox port.
// It depends only on application ports — the adapters live in the
// composition root.
type Dispatcher struct {
	outbox iam.OutboxService
	inbox  organization.InboxConsumer
	logger *slog.Logger
	cfg    DispatcherConfig
	// running is true while the Run loop is active; the readiness probe
	// (T071) reports the dispatcher module unavailable when the loop exited.
	running atomic.Bool
}

// NewDispatcher wires the loop to the delivery and inbox ports.
func NewDispatcher(outbox iam.OutboxService, inbox organization.InboxConsumer, logger *slog.Logger, cfg DispatcherConfig) *Dispatcher {
	return &Dispatcher{outbox: outbox, inbox: inbox, logger: logger, cfg: cfg}
}

// Healthy reports whether the delivery loop is running. It returns nil only
// while Run is active — a loop that exited (shutdown or failure) reads as
// unavailable for the readiness probe.
func (d *Dispatcher) Healthy() error {
	if !d.running.Load() {
		return errors.New("outbox dispatcher not running")
	}
	return nil
}

// Run claims bounded batches until ctx is cancelled. On shutdown no new
// claims start; the batch already claimed drains (each consumer call bounded
// by CallTimeout via a drain-safe context), then Run returns nil.
func (d *Dispatcher) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	d.running.Store(true)
	defer d.running.Store(false)
	for {
		claimed, err := d.outbox.ClaimOutbox(ctx, d.cfg.BatchSize, d.cfg.LeaseOwner, d.cfg.LeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return nil // shutdown aborted the claim transaction
			}
			d.logger.Warn("outbox claim failed; will retry", "error", err)
			if !sleep(ctx, d.cfg.PollInterval) {
				return nil
			}
			continue
		}
		for _, ev := range claimed {
			// Drain-safe: the parent cancel signal must not abort an
			// in-flight consumer call — it finishes up to CallTimeout.
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.CallTimeout)
			start := time.Now()
			err := d.Dispatch(callCtx, ev)
			latencyMS := time.Since(start).Milliseconds()
			cancel()
			if err != nil {
				d.logger.Error("outbox delivery failed; lease will expire and retry",
					"event_id", ev.EventID, "event_type", ev.EventType,
					"attempt", ev.AttemptCount, "dispatch_latency_ms", latencyMS, "error", err)
			} else {
				d.logger.Debug("outbox delivery done",
					"event_id", ev.EventID, "event_type", ev.EventType,
					"attempt", ev.AttemptCount, "correlation_id", ev.CorrelationID,
					"dispatch_latency_ms", latencyMS)
			}
		}
		if len(claimed) == 0 && !sleep(ctx, d.cfg.PollInterval) {
			return nil
		}
	}
}

// sleep waits d or until ctx is done; false means shutdown.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Dispatch delivers one claimed event: decode/validate the envelope v1,
// invoke the Organization handler, then ack / record failure / block with
// the claim-token CAS. A returned error means the terminal state could not
// be persisted — the lease expires and delivery is retried (at least once).
func (d *Dispatcher) Dispatch(ctx context.Context, ev iam.ClaimedEvent) error {
	// 1. decode/validate envelope v1 (single codec path). A decoded=false
	//    result means the event was durably blocked — delivery is terminal.
	consumerEvent, blocked, err := d.decode(ctx, ev)
	if err != nil {
		return err
	}
	if blocked {
		return nil
	}
	// 2. invoke the Organization inbox (its side effect + dedupe row commit
	//    in one Organization transaction).
	if err := d.inbox.HandleIAMUserDeletedV1(ctx, consumerEvent); err != nil {
		return d.recordConsumerFailure(ctx, ev, err)
	}
	// 3. claim-token CAS to published; a failure leaves the lease to expire.
	if err := d.outbox.AckOutbox(ctx, ev.EventID, ev.ClaimToken); err != nil {
		return err
	}
	return nil
}

// decode maps a claimed event to the consumer envelope, blocking durably on
// every non-retryable contract problem. It returns (_, false, nil) when the
// event was blocked successfully — a successful block is a terminal outcome,
// not an error — and (_, _, err) only when the block itself failed and the
// lease must expire for a retry.
func (d *Dispatcher) decode(ctx context.Context, ev iam.ClaimedEvent) (organization.IAMUserDeletedEvent, bool, error) {
	data, err := EncodeEnvelope(ev) // validates the shared envelope fields
	if err != nil {
		if err := d.block(ctx, ev, "malformed_envelope"); err != nil {
			return organization.IAMUserDeletedEvent{}, false, err
		}
		return organization.IAMUserDeletedEvent{}, true, nil
	}
	env, err := DecodeEnvelope(data) // idempotent wire decode
	if err != nil {
		if err := d.block(ctx, ev, "malformed_envelope"); err != nil {
			return organization.IAMUserDeletedEvent{}, false, err
		}
		return organization.IAMUserDeletedEvent{}, true, nil
	}
	consumerEvent, err := DecodeUserDeletedV1(env)
	if err != nil {
		reason := "contract_mismatch"
		if errors.Is(err, ErrUnsupportedEnvelopeVersion) {
			reason = "unsupported_version"
		}
		if err := d.block(ctx, ev, reason); err != nil {
			return organization.IAMUserDeletedEvent{}, false, err
		}
		return organization.IAMUserDeletedEvent{}, true, nil
	}
	return consumerEvent, false, nil
}

// recordConsumerFailure classifies a consumer error. Envelope/contract
// rejections from the consumer are defensive durable blocks; everything
// else is a transient failure with a safe error code and bounded backoff —
// the epoch ceiling (20 attempts / 24h, T061) is the hot-loop backstop.
func (d *Dispatcher) recordConsumerFailure(ctx context.Context, ev iam.ClaimedEvent, consumerErr error) error {
	if errors.Is(consumerErr, organization.ErrUnsupportedEventVersion) ||
		errors.Is(consumerErr, organization.ErrInvalidInput) {
		return d.block(ctx, ev, "consumer_rejected_contract")
	}
	code := "consumer_internal"
	switch {
	case errors.Is(consumerErr, organization.ErrUnavailable):
		code = "consumer_unavailable"
	case errors.Is(consumerErr, organization.ErrTimeout):
		code = "consumer_timeout"
	}
	_, err := d.outbox.RecordOutboxFailure(ctx, ev.EventID, ev.ClaimToken, code, time.Now().Add(d.backoffFor(ev.AttemptCount)))
	return err
}

// backoffFor bounds the transient backoff exponentially by claim attempt.
func (d *Dispatcher) backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := d.cfg.BackoffBase
	for i := 1; i < attempt && backoff < d.cfg.BackoffMax; i++ {
		backoff *= 2
	}
	if backoff > d.cfg.BackoffMax {
		return d.cfg.BackoffMax
	}
	return backoff
}

func (d *Dispatcher) block(ctx context.Context, ev iam.ClaimedEvent, reason string) error {
	if err := d.outbox.BlockOutbox(ctx, ev.EventID, ev.ClaimToken, reason); err != nil {
		return err
	}
	// Alert only after the durable block committed — a failed block leaves
	// the lease to expire and must not fire on every retry attempt.
	if d.cfg.AlertOnBlock != nil {
		d.cfg.AlertOnBlock(ev.EventID, ev.EventType, reason)
		return nil
	}
	d.logger.Warn("outbox event durably blocked; requires platform review",
		"event_id", ev.EventID, "event_type", ev.EventType, "block_reason", reason)
	return nil
}
