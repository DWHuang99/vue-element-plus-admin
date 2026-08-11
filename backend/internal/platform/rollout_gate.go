// Package platform — rollout-gate writer (T077, plan Phase 5.9 step 6).
//
// The writer is deliberately thin: it ticks at the sample cadence, asks the
// composition root's sample source for one observation, and records it
// through the persistence port. The window math (extension vs.
// close-and-reset on mismatch/missing sample, the 72h approval criterion)
// is owned by the migration-000012 SECURITY DEFINER function behind the
// port — the platform package never writes compatibility_rollout_gates
// directly and stays HTTP/pgx/sqlc-free. A failed source or store surfaces
// as a skipped sample: the missing-sample tolerance turns the outage into a
// window reset rather than a crash or silent evidence gap.
package platform

import (
	"context"
	"log/slog"
	"time"
)

// RolloutGateSample is one cadence observation. Assembling it (bridge mode,
// parity counts/checksums, manifest hash, mismatch counters, rollback
// artifact identity) requires touching every module, so only the composition
// root's sample source builds one — the writer records what the source
// provides and never interprets it.
type RolloutGateSample struct {
	Phase                  string
	CapabilityManifestHash string
	BridgeModeVersion      int64
	BridgeDeleteSyncOn     bool
	LegacyRows             int64
	NewRows                int64
	RowVersionChecksum     string
	MismatchCount          int64
	LastMismatchAt         *time.Time
	RollbackArtifactID     string
	RollbackSuiteResult    string
	PrincipalID            string
	ObservedAt             time.Time
	MaxGapInterval         time.Duration
}

// RolloutGateStore is the minimal persistence surface. The SECURITY DEFINER
// function behind it enforces the window math, so the writer needs nothing
// else — no reads, no direct table access.
type RolloutGateStore interface {
	RecordRolloutGateSample(ctx context.Context, s RolloutGateSample) (int64, error)
}

// RolloutGateSampleSource assembles one sample per tick. A failure is a
// skipped sample (logged by the writer); the gap tolerance converts the
// outage into a window reset.
type RolloutGateSampleSource interface {
	NextRolloutGateSample(ctx context.Context) (RolloutGateSample, error)
}

// RolloutGateWriter appends one evidence row per cadence for the lifecycle
// of ctx. Run returns nil on ctx cancellation (graceful shutdown).
type RolloutGateWriter struct {
	store   RolloutGateStore
	source  RolloutGateSampleSource
	cadence time.Duration
	logger  *slog.Logger
}

// NewRolloutGateWriter wires the writer to its store and sample source.
func NewRolloutGateWriter(store RolloutGateStore, source RolloutGateSampleSource, cadence time.Duration, logger *slog.Logger) *RolloutGateWriter {
	return &RolloutGateWriter{store: store, source: source, cadence: cadence, logger: logger}
}

// Run loops until ctx is cancelled. Each tick: assemble a sample, record it,
// log the outcome. Errors never kill the loop — a missing sample is evidence
// (window reset) by design.
func (w *RolloutGateWriter) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cadence)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			sample, err := w.source.NextRolloutGateSample(ctx)
			if err != nil {
				w.logger.Error("rollout gate sample skipped", "error", err.Error())
				continue
			}
			gateID, err := w.store.RecordRolloutGateSample(ctx, sample)
			if err != nil {
				w.logger.Error("rollout gate sample failed", "error", err.Error())
				continue
			}
			w.logger.Info("rollout gate sample recorded",
				"gate_id", gateID, "phase", sample.Phase,
				"mismatch_count", sample.MismatchCount)
		}
	}
}
