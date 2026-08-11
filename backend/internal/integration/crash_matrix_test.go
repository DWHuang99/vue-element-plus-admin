// T067 crash-matrix tests (quickstart Checkpoint F — 11 crash windows).
//
// The outbox delivery state machine on a real PostgreSQL is pinned by the
// T061 suite (backend/internal/iam/outbox_delivery_test.go) and the
// Organization handler contract by the T033/T039 suite
// (backend/internal/organization/contract_test.go). This suite drives the
// full in-process loop — real Dispatcher + real IAM OutboxDelivery adapter +
// real Organization service — against a real database, covering the windows
// that only the whole loop can show:
//
//	①  producer transaction rollback leaves no event
//	②  a stopped dispatcher keeps committed events durable pending
//	③  consumer failure rolls the inbox transaction back and the event
//	   retries; once the dependency recovers, cleanup applies exactly once
//	④  ack failure after the consumer commit lets the lease expire and
//	   redelivers without re-applying the side effect
//	⑤  concurrent workers deliver each event exactly once effectively
//	⑥  unsupported/contract-mismatch envelopes block durably
//	⑦  attempt-budget exhaustion blocks atomically through the loop
//	⑩  restart with backlog drains every retryable event
//	⑪  graceful shutdown stops claiming
//
// Windows ⑧ (crash-after-claim burns one attempt; stale claim tokens are
// rejected) and ⑨ (requeue requires a trusted recovery context and keeps
// total attempts + immutable audit) are pinned by the T061 adapter suite:
// TestClaim_ExpiredLeaseReclaimCountsAttemptCrashBudget,
// TestAck_StaleTokenRejectedLeaseIntact and
// TestRequeue_TrustedContextBumpsEpochKeepsTotalWritesEvidence.
//
// The expected outcome of the whole matrix is at-least-once delivery with
// zero lost committed events and exactly one effective Organization cleanup
// per event (quickstart.md Checkpoint F "Expected outcome").
package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // registers the postgres driver for migrate
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/db/migrations"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	iampg "github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	orgpg "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
)

// TestMain boots a shared Postgres container once; every crash window derives
// its own scratch database (crashFreshDB) so windows never share state.
func TestMain(m *testing.M) {
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("crash_matrix"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategyAndDeadline(
			60*time.Second,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start postgres container: %v\n", err)
		os.Exit(1)
	}
	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get connection string: %v\n", err)
		os.Exit(1)
	}
	crashTestConnStr = connStr

	code := m.Run()

	_ = pg.Terminate(ctx)
	os.Exit(code)
}

var crashTestConnStr string

// crashFreshDB creates a scratch database on the shared container and returns
// a connection string pointing at it; the database is dropped on cleanup.
func crashFreshDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dbname := fmt.Sprintf("crash_matrix_%d", time.Now().UnixNano())
	conn, err := pgx.Connect(ctx, crashTestConnStr)
	require.NoError(t, err)
	// DDL cannot be parameterized; the name is generated internally.
	_, err = conn.Exec(ctx, "CREATE DATABASE "+dbname)
	require.NoError(t, err)
	_ = conn.Close(ctx)
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), crashTestConnStr)
		if err == nil {
			_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbname+" WITH (FORCE)")
			_ = conn.Close(context.Background())
		}
	})
	// postgres://test:test@host:port/crash_matrix?sslmode=disable
	idx := strings.Index(crashTestConnStr, "/crash_matrix?")
	if idx == -1 {
		t.Fatal("unexpected connection string shape")
	}
	return crashTestConnStr[:idx] + "/" + dbname + crashTestConnStr[idx+len("/crash_matrix"):]
}

// crashMigrateToHead runs the full migration chain on a scratch database.
func crashMigrateToHead(t *testing.T, connStr string) {
	t.Helper()
	source, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	m, err := migrate.NewWithSourceInstance("iofs", source, connStr)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close() })
	require.NoError(t, m.Up(), "migrate to head")
}

// crashHarness wires the production adapters end to end on a scratch
// database: the real IAM OutboxDelivery port and the real Organization
// service through its real store, plus direct-sql access for seeding and
// cross-checking DB truth.
type crashHarness struct {
	connStr string
	pool    *pgxpool.Pool
	deliv   iam.OutboxService
	inbox   organization.InboxConsumer
}

func newCrashHarness(t *testing.T) *crashHarness {
	t.Helper()
	ctx := context.Background()
	connStr := crashFreshDB(t)
	crashMigrateToHead(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return &crashHarness{
		connStr: connStr,
		pool:    pool,
		deliv:   iampg.NewOutboxDelivery(pool),
		inbox:   organization.NewService(orgpg.NewStore(pool), slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

// crashEventSeed controls the seeded delivery row; zero values fall back to
// contract defaults that satisfy the DDL CHECKs.
type crashEventSeed struct {
	eventID       string
	userID        int64
	status        string // default "pending"
	eventVersion  int    // default 1
	producer      string // default "iam"
	availableAt   *time.Time
	attemptCount  int
	totalAttempts int64
}

// seedOutboxEvent inserts one pending delivery row for the IAM user delete
// contract and returns its event ID.
func (h *crashHarness) seedOutboxEvent(t *testing.T, s crashEventSeed) string {
	t.Helper()
	ctx := context.Background()
	eventID := s.eventID
	if eventID == "" {
		eventID = uuid.NewString()
	}
	status := s.status
	if status == "" {
		status = "pending"
	}
	version := s.eventVersion
	if version == 0 {
		version = 1
	}
	producer := s.producer
	if producer == "" {
		producer = "iam"
	}
	availableAt := time.Now().UTC()
	if s.availableAt != nil {
		availableAt = *s.availableAt
	}
	_, err := h.pool.Exec(ctx, `
		INSERT INTO iam_outbox_events (
			event_id, event_type, event_version, producer, aggregate_type,
			aggregate_id, aggregate_version, payload, correlation_id, occurred_at,
			status, available_at, delivery_epoch, epoch_started_at,
			attempt_count, total_attempt_count
		) VALUES ($1, 'iam.user.deleted', $2, $3, 'user', $4, 1, $5, $6, now(),
			$7, $8, 1, $8, $9, $10)`,
		eventID, version, producer, strconv.FormatInt(s.userID, 10),
		fmt.Sprintf(`{"user_id":%d}`, s.userID), uuid.NewString(),
		status, availableAt, s.attemptCount, s.totalAttempts)
	require.NoError(t, err)
	return eventID
}

// seedMembership establishes an Organization membership row for the user.
func (h *crashHarness) seedMembership(t *testing.T, userID int64) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO organization_user_departments (user_id, department_id, membership_version)
		VALUES ($1, NULL, 1)`,
		userID)
	require.NoError(t, err)
}

type crashOutboxRow struct {
	status        string
	attempt       int
	total         int64
	lastError     *string
	publishedAt   *time.Time
	blockedReason *string
}

func (h *crashHarness) outboxRow(t *testing.T, eventID string) crashOutboxRow {
	t.Helper()
	var r crashOutboxRow
	err := h.pool.QueryRow(context.Background(), `
		SELECT status, attempt_count, total_attempt_count, last_error_code,
		       published_at, blocked_reason_code
		FROM iam_outbox_events WHERE event_id = $1`, eventID).
		Scan(&r.status, &r.attempt, &r.total, &r.lastError, &r.publishedAt, &r.blockedReason)
	require.NoError(t, err)
	return r
}

func (h *crashHarness) membershipCount(t *testing.T, userID int64) int {
	t.Helper()
	var n int
	err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM organization_user_departments WHERE user_id = $1`, userID).Scan(&n)
	require.NoError(t, err)
	return n
}

func (h *crashHarness) inboxCount(t *testing.T) int {
	t.Helper()
	var n int
	err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM organization_inbox_messages`).Scan(&n)
	require.NoError(t, err)
	return n
}

// expireLease moves the row's lease into the past so the next claim reclaims
// it — simulating the wall clock passing while a worker is gone.
func (h *crashHarness) expireLease(t *testing.T, eventID string) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		`UPDATE iam_outbox_events SET leased_until = now() - interval '1 second'
		 WHERE event_id = $1`, eventID)
	require.NoError(t, err)
}

// makeDue makes a future-backoff row claimable now. epoch_started_at moves
// with available_at so the DDL CHECK (available_at >= epoch_started_at) still
// holds — for a row seeded with a future availability both were future.
func (h *crashHarness) makeDue(t *testing.T, eventID string) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		`UPDATE iam_outbox_events SET available_at = now(), epoch_started_at = now()
		 WHERE event_id = $1`, eventID)
	require.NoError(t, err)
}

// failInboxInsert arms a trigger that aborts every inbox insert, making the
// Organization handler fail mid-transaction (consumer-side crash).
func (h *crashHarness) failInboxInsert(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION crash_fail_inbox_insert() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected inbox failure'; END; $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = h.pool.Exec(ctx, `
		CREATE TRIGGER crash_fail_inbox_insert_trigger
		BEFORE INSERT ON organization_inbox_messages
		FOR EACH ROW EXECUTE FUNCTION crash_fail_inbox_insert()`)
	require.NoError(t, err)
}

// healInboxInsert removes the failing trigger.
func (h *crashHarness) healInboxInsert(t *testing.T) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		`DROP TRIGGER IF EXISTS crash_fail_inbox_insert_trigger ON organization_inbox_messages`)
	require.NoError(t, err)
}

// crashDispatcher builds a fast-polling production-config dispatcher on the
// harness's real inbox port.
func (h *crashHarness) crashDispatcher(outbox iam.OutboxService) *Dispatcher {
	cfg := DefaultDispatcherConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.CallTimeout = 500 * time.Millisecond
	cfg.BackoffBase = 10 * time.Millisecond
	cfg.BackoffMax = 50 * time.Millisecond
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewDispatcher(outbox, h.inbox, logger, cfg)
}

// runUntil drives the dispatcher loop until predicate is satisfied, then
// cancels the loop and waits for it to stop cleanly.
func (h *crashHarness) runUntil(t *testing.T, d *Dispatcher, predicate func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.Now().Add(15 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("crash-matrix predicate not satisfied within 15s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not stop cleanly after cancel")
	}
}

// ackFailOnce wraps a real OutboxService and injects one AckOutbox failure,
// delegating every other call. It simulates a dispatcher crash between the
// consumer commit and the ack CAS (window ④): the side effect is durable,
// the event stays leased, and only a lease expiry + redelivery publishes it.
type ackFailOnce struct {
	inner iam.OutboxService

	mu       sync.Mutex
	fail     bool
	ackFails int
}

func (w *ackFailOnce) ClaimOutbox(ctx context.Context, batchSize int, leaseOwner string, leaseDuration time.Duration) ([]iam.ClaimedEvent, error) {
	return w.inner.ClaimOutbox(ctx, batchSize, leaseOwner, leaseDuration)
}

func (w *ackFailOnce) AckOutbox(ctx context.Context, eventID, claimToken string) error {
	w.mu.Lock()
	fail := w.fail
	w.fail = false
	w.mu.Unlock()
	if fail {
		w.mu.Lock()
		w.ackFails++
		w.mu.Unlock()
		return errors.New("injected ack failure")
	}
	return w.inner.AckOutbox(ctx, eventID, claimToken)
}

func (w *ackFailOnce) RecordOutboxFailure(ctx context.Context, eventID, claimToken, safeErrorCode string, nextAvailableAt time.Time) (iam.OutboxDeliveryStatus, error) {
	return w.inner.RecordOutboxFailure(ctx, eventID, claimToken, safeErrorCode, nextAvailableAt)
}

func (w *ackFailOnce) BlockOutbox(ctx context.Context, eventID, claimToken, safeReasonCode string) error {
	return w.inner.BlockOutbox(ctx, eventID, claimToken, safeReasonCode)
}

func (w *ackFailOnce) RequeueBlockedOutbox(ctx context.Context, eventID string, trustedCtx iam.TrustedRecoveryContext, approvedReasonCode string, availableAt time.Time) (int64, error) {
	return w.inner.RequeueBlockedOutbox(ctx, eventID, trustedCtx, approvedReasonCode, availableAt)
}

func (w *ackFailOnce) GetOutboxBacklog(ctx context.Context) (iam.OutboxBacklog, error) {
	return w.inner.GetOutboxBacklog(ctx)
}

func (w *ackFailOnce) ackFailureCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ackFails
}

// --- Window ①: producer rollback --------------------------------------------

// A producer transaction that wrote the outbox row and then rolled back must
// leave zero trace: event durability starts only at the delete transaction
// commit, never at the outbox write itself.
func TestCrashMatrix_ProducerRollbackLeavesNoEvent(t *testing.T) {
	h := newCrashHarness(t)
	ctx := context.Background()

	id := uuid.NewString()
	tx, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		INSERT INTO iam_outbox_events (
			event_id, event_type, event_version, producer, aggregate_type,
			aggregate_id, aggregate_version, payload, correlation_id, occurred_at,
			status, available_at, delivery_epoch, epoch_started_at
		) VALUES ($1, 'iam.user.deleted', 1, 'iam', 'user', '42', 1,
			'{"user_id":42}', 'rollback-test', now(), 'pending', now(), 1, now())`,
		id)
	require.NoError(t, err)
	// The delete command fails after the outbox write → the whole transaction
	// (side effect + event) rolls back atomically.
	require.NoError(t, tx.Rollback(ctx))

	var n int
	err = h.pool.QueryRow(ctx,
		`SELECT count(*) FROM iam_outbox_events WHERE event_id = $1`, id).Scan(&n)
	require.NoError(t, err)
	require.Zero(t, n, "a rolled-back producer transaction must leave no event")

	// Control: a committed write is durable and claimable.
	committed := h.seedOutboxEvent(t, crashEventSeed{userID: 42})
	row := h.outboxRow(t, committed)
	require.Equal(t, "pending", row.status)
	require.Zero(t, row.attempt)
}

// --- Window ②: stopped dispatcher -------------------------------------------

// With no dispatcher running, committed events stay durable pending: nothing
// is lost, nothing changes, and a later dispatcher run drains them.
func TestCrashMatrix_StoppedDispatcherKeepsEventDurablePending(t *testing.T) {
	h := newCrashHarness(t)
	ctx := context.Background()
	id := h.seedOutboxEvent(t, crashEventSeed{userID: 42})

	// No dispatcher is ever started in this window — the committed event must
	// survive untouched and still claimable later.
	row := h.outboxRow(t, id)
	require.Equal(t, "pending", row.status)
	require.Zero(t, row.attempt)

	backlog, err := h.deliv.GetOutboxBacklog(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), backlog.PendingCount, "one durable pending event")
	require.Zero(t, backlog.PublishedCount)

	// Recovery path: the restarted loop drains the backlog.
	h.runUntil(t, h.crashDispatcher(h.deliv), func() bool { return h.outboxRow(t, id).status == "published" })
	require.NotNil(t, h.outboxRow(t, id).publishedAt)
}

// --- Window ③: consumer failure rolls back and retries -----------------------

// A failing consumer rolls the whole inbox transaction back (no successful
// row, membership intact), the dispatcher records one failure with bounded
// backoff, and once the dependency recovers the retry applies the cleanup
// exactly once.
func TestCrashMatrix_ConsumerFailureRollsBackAndRetries(t *testing.T) {
	h := newCrashHarness(t)
	userID := int64(7)
	id := h.seedOutboxEvent(t, crashEventSeed{userID: userID})
	h.seedMembership(t, userID)

	h.failInboxInsert(t)
	defer h.healInboxInsert(t)

	// The handler call fails → the inbox transaction rolls back and the event
	// returns to pending with one recorded attempt and a backoff.
	h.runUntil(t, h.crashDispatcher(h.deliv), func() bool {
		row := h.outboxRow(t, id)
		return row.status == "pending" && row.attempt == 1 && row.lastError != nil
	})
	require.Equal(t, 1, h.membershipCount(t, userID), "membership must survive a rolled-back handler")
	require.Zero(t, h.inboxCount(t), "no successful inbox row may survive a rolled-back handler")

	// Dependency recovers: the same loop retries after the bounded backoff
	// and the cleanup applies exactly once.
	h.healInboxInsert(t)
	h.runUntil(t, h.crashDispatcher(h.deliv), func() bool { return h.outboxRow(t, id).status == "published" })
	require.Zero(t, h.membershipCount(t, userID))
	require.Equal(t, 1, h.inboxCount(t))
	row := h.outboxRow(t, id)
	require.Equal(t, 2, row.attempt, "one failed claim + one successful retry")
	require.Equal(t, int64(2), row.total)
}

// --- Window ④: ack failure after consumer commit -----------------------------

// The consumer commit is durable before the ack; an ack failure leaves the
// event leased. When the lease expires the event redelivers, and the dedupe
// row makes the replay effect-free: exactly one effective cleanup, one inbox
// row, and a published transition only after the second (successful) ack.
func TestCrashMatrix_AckFailureRedeliversIdempotently(t *testing.T) {
	h := newCrashHarness(t)
	userID := int64(8)
	id := h.seedOutboxEvent(t, crashEventSeed{userID: userID})
	h.seedMembership(t, userID)

	wrapped := &ackFailOnce{inner: h.deliv}
	wrapped.mu.Lock()
	wrapped.fail = true
	wrapped.mu.Unlock()

	// Run until the ack attempt has actually failed (the consumer commit and
	// the failed ack are both behind the side effect being present).
	h.runUntil(t, h.crashDispatcher(wrapped), func() bool {
		return wrapped.ackFailureCount() == 1
	})

	// The side effect is durable, but the event must NOT be published — the
	// ack CAS never happened.
	require.Zero(t, h.membershipCount(t, userID), "consumer commit is durable")
	require.Equal(t, 1, h.inboxCount(t), "consumer commit wrote the dedupe row")
	row := h.outboxRow(t, id)
	require.Equal(t, "leased", row.status, "the failed ack leaves the lease untouched")
	require.Nil(t, row.publishedAt)

	// The worker is gone; time passes, the lease expires, and the loop
	// redelivers. The dedupe replay is effect-free.
	h.expireLease(t, id)
	h.runUntil(t, h.crashDispatcher(wrapped), func() bool { return h.outboxRow(t, id).status == "published" })
	row = h.outboxRow(t, id)
	require.Equal(t, 2, row.attempt, "redelivery is a second claim")
	require.Equal(t, int64(2), row.total)
	require.Zero(t, h.membershipCount(t, userID), "redelivery must not re-apply the side effect")
	require.Equal(t, 1, h.inboxCount(t), "the dedupe replay must not add an inbox row")
	require.NotNil(t, row.publishedAt)
}

// --- Window ⑤: concurrent workers -------------------------------------------

// Two dispatcher loops with independent connection pools (two processes)
// race over one event. The SKIP LOCKED claim admits exactly one lease; the
// loser polls an empty batch. One effective cleanup, one inbox row.
func TestCrashMatrix_ConcurrentWorkersDeliverOnce(t *testing.T) {
	h := newCrashHarness(t)
	userID := int64(9)
	id := h.seedOutboxEvent(t, crashEventSeed{userID: userID})
	h.seedMembership(t, userID)

	// Second worker: its own pool, adapter and service — like another process.
	pool2, err := pgxpool.New(context.Background(), h.connStr)
	require.NoError(t, err)
	defer pool2.Close()
	deliv2 := iampg.NewOutboxDelivery(pool2)
	inbox2 := organization.NewService(orgpg.NewStore(pool2), slog.New(slog.NewTextHandler(io.Discard, nil)))

	d1 := h.crashDispatcher(h.deliv)
	cfg := DefaultDispatcherConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.CallTimeout = 500 * time.Millisecond
	cfg.BackoffBase = 10 * time.Millisecond
	cfg.BackoffMax = 50 * time.Millisecond
	d2 := NewDispatcher(deliv2, inbox2, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { done1 <- d1.Run(ctx) }()
	go func() { done2 <- d2.Run(ctx) }()

	require.Eventually(t, func() bool { return h.outboxRow(t, id).status == "published" },
		15*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done1)
	require.NoError(t, <-done2)

	row := h.outboxRow(t, id)
	require.Equal(t, "published", row.status)
	require.Zero(t, h.membershipCount(t, userID), "cleanup must apply exactly once")
	require.Equal(t, 1, h.inboxCount(t), "exactly one processed inbox row")
	require.Equal(t, int64(1), row.total, "a single claim — the losing worker never steals the lease")
}

// --- Window ⑥: unsupported contracts block durably ---------------------------

// Envelope version and producer contract are checked before the Organization
// port is ever invoked; a mismatched event lands in blocked with a stable
// reason code and never hot-loops.
func TestCrashMatrix_UnsupportedContractBlocksDurably(t *testing.T) {
	for name, seed := range map[string]crashEventSeed{
		"unsupported version": {userID: 10, eventVersion: 2},
		"wrong producer":      {userID: 10, producer: "rbac"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newCrashHarness(t)
			userID := int64(10)
			id := h.seedOutboxEvent(t, crashEventSeed{userID: userID, eventVersion: seed.eventVersion, producer: seed.producer})
			h.seedMembership(t, userID)

			h.runUntil(t, h.crashDispatcher(h.deliv), func() bool {
				row := h.outboxRow(t, id)
				return row.status == "blocked" && row.blockedReason != nil
			})
			row := h.outboxRow(t, id)
			want := "unsupported_version"
			if seed.producer != "" {
				want = "contract_mismatch"
			}
			require.Equal(t, want, *row.blockedReason)
			require.Zero(t, h.inboxCount(t), "the consumer must never see a mismatched envelope")
			require.Equal(t, 1, h.membershipCount(t, userID), "no side effect for a mismatched envelope")
			require.Equal(t, 1, row.attempt, "blocking consumes the one claim — no retry loop")
		})
	}
}

// --- Window ⑦: attempt budget exhaustion blocks atomically --------------------

// The 20th failed claim of a delivery epoch transitions straight to blocked
// in the same transaction that records the failure — never
// pending-then-blocked — so there is no retry storm and no stuck pending row.
func TestCrashMatrix_ExhaustedBudgetBlocksThroughLoop(t *testing.T) {
	h := newCrashHarness(t)
	userID := int64(11)
	id := h.seedOutboxEvent(t, crashEventSeed{userID: userID, attemptCount: 19, totalAttempts: 19})
	h.seedMembership(t, userID)
	h.failInboxInsert(t)
	defer h.healInboxInsert(t)

	h.runUntil(t, h.crashDispatcher(h.deliv), func() bool { return h.outboxRow(t, id).status == "blocked" })
	row := h.outboxRow(t, id)
	require.Equal(t, "delivery_exhausted", *row.blockedReason)
	require.Equal(t, int64(20), row.total, "the 20th attempt is the blocking one")
	require.Zero(t, h.inboxCount(t), "the failed handler never applied a side effect")
	require.Equal(t, 1, h.membershipCount(t, userID), "the failed handler never applied a side effect")
}

// --- Window ⑩: restart with backlog -------------------------------------------

// Events still in backoff when a dispatcher stops remain pending; the
// restarted loop drains the whole backlog once every event is due — nothing
// committed is ever lost.
func TestCrashMatrix_RestartWithBacklogDrains(t *testing.T) {
	h := newCrashHarness(t)
	// User 1 is due now; users 2 and 3 are still in backoff when the first
	// run starts (available in the future).
	id1 := h.seedOutboxEvent(t, crashEventSeed{userID: 1})
	future := time.Now().Add(time.Hour)
	id2 := h.seedOutboxEvent(t, crashEventSeed{userID: 2, availableAt: &future})
	id3 := h.seedOutboxEvent(t, crashEventSeed{userID: 3, availableAt: &future})
	h.seedMembership(t, 1)
	h.seedMembership(t, 2)
	h.seedMembership(t, 3)

	// First run: drains what is available, leaves the backlog untouched.
	h.runUntil(t, h.crashDispatcher(h.deliv), func() bool { return h.outboxRow(t, id1).status == "published" })
	require.Equal(t, "pending", h.outboxRow(t, id2).status, "backlogged event must stay pending")
	require.Equal(t, "pending", h.outboxRow(t, id3).status)
	require.Equal(t, 1, h.membershipCount(t, 2), "backlogged event must not be processed early")

	// The dispatcher is down while time passes; the restarted loop drains
	// everything that has come due.
	h.makeDue(t, id2)
	h.makeDue(t, id3)
	h.runUntil(t, h.crashDispatcher(h.deliv), func() bool {
		return h.outboxRow(t, id2).status == "published" && h.outboxRow(t, id3).status == "published"
	})
	for _, id := range []string{id1, id2, id3} {
		require.Equal(t, "published", h.outboxRow(t, id).status)
	}
	require.Zero(t, h.membershipCount(t, 1))
	require.Zero(t, h.membershipCount(t, 2))
	require.Zero(t, h.membershipCount(t, 3))
	require.Equal(t, 3, h.inboxCount(t))
}

// --- Window ⑪: graceful shutdown ----------------------------------------------

// Run stops cleanly on cancel: the delivered event stays published (no
// redelivery), a cancelled Run returns nil immediately, and nothing is
// claimed after the shutdown takes effect.
func TestCrashMatrix_GracefulShutdownStopsClaiming(t *testing.T) {
	h := newCrashHarness(t)
	id := h.seedOutboxEvent(t, crashEventSeed{userID: 12})
	h.seedMembership(t, 12)
	d := h.crashDispatcher(h.deliv)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Let the loop claim and deliver the one event, then shut it down.
	require.Eventually(t, func() bool { return h.outboxRow(t, id).status == "published" },
		15*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "Run must return nil after a clean shutdown")
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	// A cancelled Run returns nil immediately and claims nothing more.
	require.NoError(t, d.Run(ctx))

	// The delivered event was never redelivered after the shutdown.
	row := h.outboxRow(t, id)
	require.Equal(t, "published", row.status)
	require.Equal(t, int64(1), row.total)
	require.Zero(t, h.membershipCount(t, 12))
	require.Equal(t, 1, h.inboxCount(t))
}
