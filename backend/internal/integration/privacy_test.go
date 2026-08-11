// T068 privacy scan (contracts/domain-events.md §Principles): payload is
// minimal and excludes passwords, tokens, hashes and unnecessary PII;
// correlation_id must not be a raw session token; logs may include event ID,
// type/version, aggregate ID, attempt number, correlation ID and safe error
// code — payload logging defaults to disabled.
//
// Dynamic side on a real database (reuses the crash-matrix harness):
// claimed rows carry only the minimal payload, correlation IDs are bounded
// identifiers never derived from lease/claim tokens, and every dispatcher
// log path (success / consumer failure / durable block) is free of payload
// content and credential/PII vocabulary.
package integration

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// secretRe matches credential/PII vocabulary that must never appear in
// outbox rows or delivery logs.
var secretRe = regexp.MustCompile(`(?i)pass(word|wd)?|secret|token|hash|salt|email|username|credential`)

// privacyDispatcher builds a fast-polling dispatcher on the harness's real
// ports with the given logger (used to capture delivery logs).
func privacyDispatcher(h *crashHarness, logger *slog.Logger) *Dispatcher {
	cfg := DefaultDispatcherConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.CallTimeout = 500 * time.Millisecond
	cfg.BackoffBase = 10 * time.Millisecond
	cfg.BackoffMax = 50 * time.Millisecond
	return NewDispatcher(h.deliv, h.inbox, logger, cfg)
}

// TestPrivacy_OutboxRowCarriesOnlyMinimalPayload pins the stored event: the
// claimed payload is exactly {"user_id": ...}, correlation_id follows the
// bounded identifier grammar and is never derived from the lease/claim
// token, and the stored payload text carries no credential/PII vocabulary.
func TestPrivacy_OutboxRowCarriesOnlyMinimalPayload(t *testing.T) {
	h := newCrashHarness(t)
	id := h.seedOutboxEvent(t, crashEventSeed{userID: 42})

	evs, err := h.deliv.ClaimOutbox(context.Background(), 10, "privacy-scan", 30*time.Second)
	require.NoError(t, err)
	require.Len(t, evs, 1)
	ev := evs[0]

	// Minimal payload: only the opaque IAM subject id.
	for k := range ev.Payload {
		require.Equal(t, "user_id", k, "no PII/credential keys may enter an outbox payload")
	}

	// Correlation is a bounded request/workflow identifier — never the
	// lease/claim token and never the event id.
	require.Regexp(t, correlationRe, ev.CorrelationID)
	require.NotEqual(t, ev.ClaimToken, ev.CorrelationID, "correlation must not be derived from the claim token")
	require.NotEqual(t, ev.EventID, ev.CorrelationID)

	// The stored row's payload text carries no secret vocabulary.
	var payload string
	err = h.pool.QueryRow(context.Background(),
		`SELECT payload::text FROM iam_outbox_events WHERE event_id = $1`, id).Scan(&payload)
	require.NoError(t, err)
	require.Contains(t, payload, "user_id")
	require.NotRegexp(t, secretRe, payload)
}

// TestPrivacy_DispatcherLogsExcludePayloadAndSecrets drives all three log
// paths — success, consumer failure, durable block — against a captured
// logger and asserts the whole output carries no payload content and no
// credential/PII vocabulary.
func TestPrivacy_DispatcherLogsExcludePayloadAndSecrets(t *testing.T) {
	h := newCrashHarness(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Success path: published with debug latency logging.
	id1 := h.seedOutboxEvent(t, crashEventSeed{userID: 50})
	h.seedMembership(t, 50)
	h.runUntil(t, privacyDispatcher(h, logger), func() bool { return h.outboxRow(t, id1).status == "published" })

	// Consumer failure path: the inbox transaction fails, the dispatcher
	// logs the failure with a safe error code and bounded backoff.
	h.failInboxInsert(t)
	id2 := h.seedOutboxEvent(t, crashEventSeed{userID: 51})
	h.seedMembership(t, 51)
	h.runUntil(t, privacyDispatcher(h, logger), func() bool {
		row := h.outboxRow(t, id2)
		return row.status == "pending" && row.attempt == 1 && row.lastError != nil
	})
	h.healInboxInsert(t)

	// Durable block path: unsupported version blocks with a stable reason.
	id3 := h.seedOutboxEvent(t, crashEventSeed{userID: 52, eventVersion: 2})
	h.runUntil(t, privacyDispatcher(h, logger), func() bool {
		row := h.outboxRow(t, id3)
		return row.status == "blocked" && row.blockedReason != nil
	})

	logs := buf.String()
	require.NotRegexp(t, secretRe, logs, "delivery logs must not carry credential/PII vocabulary")
	require.NotContains(t, logs, "user_id", "payload content must never be logged")
	require.NotContains(t, logs, "payload", "the payload field must never be logged")
	require.NotContains(t, logs, `"42"`, "no aggregate id value may leak into logs")
}
