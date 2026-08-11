//go:build rollback

package adminapi

// TestWire_* legacy-router wire tests (T075/T074/T076), rollback build only:
// they drive the legacy monolith router, the shadow-read replay and the legacy
// delete delegation that the shipped binary no longer compiles.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/server"
)

// TestWire_ShadowReadsEndToEnd proves the T075 wiring against the real
// composition root: with shadow reads enabled the BFF /roles route replays
// through the legacy router built alongside, both authenticate the same
// seeded session, and the responses agree (shadow_reads_total counts,
// no mismatch counter exists).
func TestWire_ShadowReadsEndToEnd(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	// Seed a user with the migrated 'admin' role and a session whose token hash
	// both the BFF Authenticate middleware and the legacy middleware.Auth read
	// from the sessions table (token_hash = sha256 hex, per iam.HashToken).
	// (pgx prepares single statements, so the seed runs as separate Execs.)
	const token = "shadow-probe-token"
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	// 000006 dropped the temporary lifecycle defaults, so the seed states
	// lifecycle_state and version explicitly (legacy queries were updated in
	// lockstep; the IAM service sets both on creation).
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version)
		VALUES ('shadow_probe', 'not-a-real-hash', 'active', 1)`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
			SELECT u.id, r.id FROM users u, roles r
			WHERE u.username = 'shadow_probe' AND r.code = 'admin'`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at)
			SELECT $1, u.id, now() + interval '1 hour' FROM users u
			WHERE u.username = 'shadow_probe'`, tokenHash)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWire(db, logger, Config{
		AdminBFFRoutesEnabled: true,
		ShadowReadsEnabled:    true,
	}, nil, server.LegacyRateLimitConfig{})
	router, closeFn := w.Router()
	defer closeFn()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "BFF serves roles to the seeded admin")
	require.Contains(t, rec.Body.String(), `"admin"`, "the role list carries the seeded admin role")

	// The next scrape exposes the shadow verdict: one replay, no divergence
	// (the mismatch counter is only created when a mismatch occurs).
	metrics := httptest.NewRecorder()
	router.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, metrics.Code)
	require.Contains(t, metrics.Body.String(), "shadow_reads_total 1\n")
	require.NotContains(t, metrics.Body.String(), "shadow_reads_mismatches_total")
}

func TestWire_LegacyRouterMountsMetrics(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWire(db, logger, Config{}, nil, server.LegacyRateLimitConfig{})
	router, closeFn := w.Router()
	defer closeFn()

	// The recorder middleware counts each request after it completes, so the
	// first /metrics scrape proves the endpoint + collectors, and the second
	// one exposes the first scrape in http_requests_total.
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, first.Code)

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, second.Code)
	require.Contains(t, second.Body.String(), "http_requests_total 1\n")
}

// TestWire_LegacyDeleteDelegationEndToEnd proves the T076 wiring against the
// real composition root: with LEGACY_DELETE_IAM_DELEGATION_ENABLED the legacy
// POST /api/v1/users/delete route delegates to the IAM DeleteUsers port. The
// outbox event and the command receipt exist only on the IAM path — direct
// legacy deletes never write them — so their presence proves the delegation
// ran, not the direct delete.
func TestWire_LegacyDeleteDelegationEndToEnd(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	// Seed the acting admin and one target user, both active with the 'admin'
	// role join and a session whose token hash the legacy middleware.Auth reads.
	const token = "delete-delegation-token"
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version)
		VALUES ('del_admin', 'not-a-real-hash', 'active', 1)`)
	require.NoError(t, err)
	var targetID int64
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version)
		VALUES ('del_target', 'not-a-real-hash', 'active', 1) RETURNING id`).Scan(&targetID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
			SELECT u.id, r.id FROM users u, roles r
			WHERE u.username = 'del_admin' AND r.code = 'admin'`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at)
			SELECT $1, u.id, now() + interval '1 hour' FROM users u
			WHERE u.username = 'del_admin'`, tokenHash)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWire(db, logger, Config{
		LegacyDeleteIAMDelegationEnabled: true,
	}, nil, server.LegacyRateLimitConfig{})
	router, closeFn := w.Router()
	defer closeFn()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/delete",
		strings.NewReader(fmt.Sprintf(`{"ids":[%d]}`, targetID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	// The RequestID middleware echoes this header into the gin context; the
	// delegate must pass that validated value into the IAM operation audit.
	req.Header.Set("X-Request-Id", "corr-wire-del")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"data":{}`)

	// 1. The row is gone (sessions/user_roles cascade with it).
	var n int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM users WHERE id = $1", targetID).Scan(&n))
	require.Zero(t, n, "target user deleted")

	// 2. The iam.user.deleted v1 outbox event exists, carrying the tombstone
	// version (prior 1 + 1) and the correlation ID from the HTTP request.
	var eventType, corrID string
	var aggVersion int64
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT event_type, correlation_id, aggregate_version
		FROM iam_outbox_events WHERE aggregate_id = $1`,
		strconv.FormatInt(targetID, 10)).Scan(&eventType, &corrID, &aggVersion))
	require.Equal(t, "iam.user.deleted", eventType)
	require.Equal(t, "corr-wire-del", corrID, "correlation ID flows from the request into the outbox event")
	require.Equal(t, int64(2), aggVersion, "tombstone version = prior version + 1")

	// 3. The batch receipt committed atomically with deletion and events.
	var status string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT status FROM iam_command_receipts WHERE command_name = 'delete_users'`).Scan(&status))
	require.Equal(t, "succeeded", status)
}

// TestWire_LegacyDeleteDirectNoDelegate is the contrast case: without the
// delegate the route keeps the pre-split direct delete — the row is gone, but
// no outbox event and no receipt are written, proving those artifacts in the
// delegation test are produced by the IAM path.
func TestWire_LegacyDeleteDirectNoDelegate(t *testing.T) {
	ctx := context.Background()
	connStr := wireSmokeDB(t)

	db, err := database.Connect(ctx, database.Config{URL: connStr, MaxConns: 5, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	const token = "direct-delete-token"
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version)
		VALUES ('dir_admin', 'not-a-real-hash', 'active', 1)`)
	require.NoError(t, err)
	var targetID int64
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, lifecycle_state, version)
		VALUES ('dir_target', 'not-a-real-hash', 'active', 1) RETURNING id`).Scan(&targetID))
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
			SELECT u.id, r.id FROM users u, roles r
			WHERE u.username = 'dir_admin' AND r.code = 'admin'`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at)
			SELECT $1, u.id, now() + interval '1 hour' FROM users u
			WHERE u.username = 'dir_admin'`, tokenHash)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWire(db, logger, Config{}, nil, server.LegacyRateLimitConfig{})
	router, closeFn := w.Router()
	defer closeFn()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/delete",
		strings.NewReader(fmt.Sprintf(`{"ids":[%d]}`, targetID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"data":{}`)

	var n int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM users WHERE id = $1", targetID).Scan(&n))
	require.Zero(t, n, "direct delete removes the row")

	var events int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM iam_outbox_events").Scan(&events))
	require.Zero(t, events, "direct delete writes no outbox events")

	var receipts int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		"SELECT count(*) FROM iam_command_receipts").Scan(&receipts))
	require.Zero(t, receipts, "direct delete writes no command receipts")
}
