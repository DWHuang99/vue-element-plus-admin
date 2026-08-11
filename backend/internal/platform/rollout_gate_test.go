// Rollout-gate writer loop tests (T077). The writer is a pure policy loop:
// tick → sample from the source → record through the store. The window math
// itself is owned by the migration-000012 SQL function and verified in
// internal/database/rollout_gate_test.go; here only the loop behavior is
// asserted with fakes — errors from either side must never kill the loop (a
// missing sample is evidence, the gap tolerance turns it into a window
// reset), and ctx cancellation must stop it cleanly.
package platform

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGateSource assembles a fixed sample; errMode makes the next call fail.
type fakeGateSource struct {
	calls   atomic.Int32
	errMode atomic.Bool
}

func (s *fakeGateSource) NextRolloutGateSample(ctx context.Context) (RolloutGateSample, error) {
	s.calls.Add(1)
	if s.errMode.Load() {
		return RolloutGateSample{}, errors.New("fixture source failure")
	}
	return RolloutGateSample{Phase: "writer-test", ObservedAt: time.Now()}, nil
}

// fakeGateStore records samples; errMode makes the next call fail.
type fakeGateStore struct {
	recorded atomic.Int32
	errMode  atomic.Bool
}

func (s *fakeGateStore) RecordRolloutGateSample(ctx context.Context, smp RolloutGateSample) (int64, error) {
	s.recorded.Add(1)
	if s.errMode.Load() {
		return 0, errors.New("fixture store failure")
	}
	return int64(s.recorded.Load()), nil
}

// runWriter starts the writer on a cancelable ctx and returns the cancel
// func and a channel that receives the Run result once the loop exits.
func runWriter(t *testing.T, store RolloutGateStore, source RolloutGateSampleSource, cadence time.Duration) (cancel context.CancelFunc, done chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := NewRolloutGateWriter(store, source, cadence, slog.New(slog.DiscardHandler))
	done = make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(cancel)
	return cancel, done
}

// stopWriter cancels the writer and asserts Run exited with nil.
func stopWriter(t *testing.T, cancel context.CancelFunc, done chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "Run must return nil on cancellation")
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not stop within 5s of cancellation")
	}
}

// TestRolloutGateWriter_RecordsOnEachTick: the loop samples at the cadence
// and records every sample; cancellation stops it with nil.
func TestRolloutGateWriter_RecordsOnEachTick(t *testing.T) {
	source := &fakeGateSource{}
	store := &fakeGateStore{}
	cancel, done := runWriter(t, store, source, 10*time.Millisecond)

	require.Eventually(t, func() bool { return store.recorded.Load() >= 3 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, store.recorded.Load(), source.calls.Load(),
		"every sampled tick is recorded — one row per sample")
	// A couple more ticks keep flowing while the loop lives.
	before := store.recorded.Load()
	require.Eventually(t, func() bool { return store.recorded.Load() > before }, 5*time.Second, 10*time.Millisecond)

	stopWriter(t, cancel, done)
}

// TestRolloutGateWriter_SourceErrorSkipsWithoutStoreCall: a source failure
// is a skipped sample — the store is not called and the loop continues.
func TestRolloutGateWriter_SourceErrorSkipsWithoutStoreCall(t *testing.T) {
	source := &fakeGateSource{}
	store := &fakeGateStore{}
	cancel, done := runWriter(t, store, source, 10*time.Millisecond)

	// First sample: healthy (proves the loop is ticking and the store is
	// reached when the source succeeds), then poison the source. Snapshot the
	// recorded count here: the loop may already have one healthy sample in
	// flight (source read before errMode flipped), which can still land once
	// after the flip — never assert a hard count at the poison boundary.
	require.Eventually(t, func() bool { return store.recorded.Load() >= 1 }, 5*time.Second, 10*time.Millisecond)
	recordedAtPoison := store.recorded.Load()
	source.errMode.Store(true)
	poisonedAt := source.calls.Load()

	// Two consecutive poisoned samples must be skipped — neither reaches the
	// store. The loop keeps ticking past the failures.
	require.Eventually(t, func() bool { return source.calls.Load() >= poisonedAt+2 }, 5*time.Second, 10*time.Millisecond)
	assert.Greater(t, source.calls.Load(), poisonedAt,
		"the loop keeps ticking after the source failure")

	// Let any in-flight healthy sample land, then run several more poisoned
	// ticks: the count must never grow past recordedAtPoison+1 (the at-most-one
	// sample that was in flight when errMode flipped).
	require.Eventually(t, func() bool { return store.recorded.Load() <= recordedAtPoison+1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond) // a few more failed ticks
	assert.LessOrEqual(t, store.recorded.Load(), recordedAtPoison+1,
		"source failures never reach the store — the sample is skipped")

	frozen := store.recorded.Load()
	source.errMode.Store(false)
	require.Eventually(t, func() bool { return store.recorded.Load() > frozen }, 5*time.Second, 10*time.Millisecond,
		"the loop recovers once the source is healthy again")
	stopWriter(t, cancel, done)
}

// TestRolloutGateWriter_StoreErrorContinuesLoop: a store failure is logged
// and the loop keeps sampling — the next tick records again.
func TestRolloutGateWriter_StoreErrorContinuesLoop(t *testing.T) {
	source := &fakeGateSource{}
	store := &fakeGateStore{}
	cancel, done := runWriter(t, store, source, 10*time.Millisecond)

	require.Eventually(t, func() bool { return store.recorded.Load() >= 1 }, 5*time.Second, 10*time.Millisecond)
	store.errMode.Store(true)
	require.Eventually(t, func() bool { return store.recorded.Load() >= 2 }, 5*time.Second, 10*time.Millisecond,
		"the store keeps being called after a failure — errors never kill the loop")
	assert.Equal(t, store.recorded.Load(), source.calls.Load(),
		"store errors do not drop samples from the source side")
	stopWriter(t, cancel, done)
}

// TestRolloutGateWriter_CancelStopsWithoutSampling: cancellation before the
// first tick exits the loop with nil and records nothing.
func TestRolloutGateWriter_CancelStopsWithoutSampling(t *testing.T) {
	source := &fakeGateSource{}
	store := &fakeGateStore{}
	cancel, done := runWriter(t, store, source, time.Hour) // far-future tick

	cancel() // cancel before any tick could fire
	select {
	case err := <-done:
		require.NoError(t, err, "Run returns nil on cancellation")
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit on cancellation")
	}
	assert.Equal(t, int32(0), store.recorded.Load(), "no tick elapsed, nothing recorded")
	assert.Equal(t, int32(0), source.calls.Load(), "no tick elapsed, nothing sampled")
}
