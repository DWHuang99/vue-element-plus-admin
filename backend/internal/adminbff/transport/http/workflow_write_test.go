// T056 managed-user idempotency-key HTTP contract tests
// (contracts/http-api-compatibility.md Managed-user idempotency header).
//
// The suite runs the BFF router against the real IAM/Organization services
// and the real workflow store on the shared migrated container from
// compat_suite_test.go: header validation, same-key replay/continue,
// conflicting safe fields 409 IDEMPOTENCY_CONFLICT and a running workflow
// 409 OPERATION_IN_PROGRESS with Retry-After. The running/awaiting fixtures
// are inserted directly so the idempotency scope resolves without needing
// failure injection in the transport.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/consistency"
)

// newWriteEnv builds a fresh BFF-router environment (authenticated admin)
// for the managed-user write tests.
func newWriteEnv(t *testing.T, prefix string) *testEnv {
	t.Helper()
	e := newTestEnv(t, prefix)
	e.router, _ = buildBFFRouter(t, e)
	e.seed(t)
	return e
}

// doJSONKey is doJSON with the Idempotency-Key header, returning the raw
// response headers so Retry-After presence/absence is assertable.
func doJSONKey(t *testing.T, r *gin.Engine, method, path, token, key string, body any) (int, http.Header, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var parsed map[string]any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
	}
	return w.Code, w.Header(), parsed
}

func TestWorkflowWrite_InvalidIdempotencyKey(t *testing.T) {
	e := newWriteEnv(t, "idem_invalid")

	// Charset rule: 16–128 of [A-Za-z0-9._:-]; anything else (too short,
	// too long, space, '+', non-ASCII) is a 400 AUTH_INVALID_INPUT.
	for _, key := range []string{
		"short",                  // <16
		strings.Repeat("a", 129), // >128
		"aaaaaaaaaaaaaaaa ",      // space not in charset
		"aaaaaaaaaaaaaaaa+",      // + not in charset
		"aaaaaaaaaaaaaaaa中",      // non-ASCII not in charset
		strings.Repeat("a", 15),  // boundary below
	} {
		status, _, body := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key,
			map[string]any{"username": "idem_bad", "password": "password_123", "roles": []int64{}})
		requireError(t, status, body, http.StatusBadRequest, "AUTH_INVALID_INPUT")
	}

	// Same rule on the delete endpoint.
	status, _, body := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users/delete", e.admin.token, "bad",
		map[string]any{"ids": []int64{1}})
	requireError(t, status, body, http.StatusBadRequest, "AUTH_INVALID_INPUT")

	// Authentication/route authorization precede idempotency validation: the
	// plain actor's invalid key is still 403 (never leaks the header rules).
	status, _, body = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.plain.token, "bad",
		map[string]any{"username": "idem_bad", "password": "password_123", "roles": []int64{}})
	requireError(t, status, body, http.StatusForbidden, "AUTH_FORBIDDEN")
}

func TestWorkflowWrite_CreateReplayAndConflict(t *testing.T) {
	e := newWriteEnv(t, "idem_create")
	key := "8f9703d0-04a7-4d4a-a75a-8807eb733961"
	body := map[string]any{"username": "idem_replay_user", "password": "password_123", "roles": []int64{}}

	// First submission creates the user; the same key + same safe fields
	// replays the stored success without a second side effect.
	status, _, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, body)
	require.Equal(t, http.StatusOK, status, "create: %v", resp)
	assert.Equal(t, map[string]any{}, resp["data"])

	status, _, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, body)
	require.Equal(t, http.StatusOK, status, "replay: %v", resp)
	assert.Equal(t, map[string]any{}, resp["data"])

	// Exactly one user row exists (the replay did not duplicate side effects).
	var count int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE username = 'idem_replay_user'`).Scan(&count))
	assert.Equal(t, int64(1), count, "replay must not create a second user")

	// Same key + conflicting safe field (different username) is 409
	// IDEMPOTENCY_CONFLICT with no Retry-After (a conflict is not a retry).
	status, headers, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key,
		map[string]any{"username": "idem_other_user", "password": "password_123", "roles": []int64{}})
	requireError(t, status, resp, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
	assert.Empty(t, headers.Get("Retry-After"), "IDEMPOTENCY_CONFLICT carries no Retry-After")
}

func TestWorkflowWrite_CreateRunningOperationInProgress(t *testing.T) {
	e := newWriteEnv(t, "idem_running")
	key := "running-key-00000000000001"
	actor := e.admin.id
	username := "idem_running_user"
	fp, err := consistency.FingerprintV1("managed_user.create", actor, map[string]any{
		"username":                  username,
		"account":                   consistency.OptString(nil),
		"email":                     consistency.OptString(nil),
		"role_ids":                  consistency.SortedIDs(nil),
		"department_id":             consistency.OptInt(nil),
		"password_change_requested": true,
	})
	require.NoError(t, err)

	// A claimed running workflow already scoped to (actor, key): the same
	// request must not start a duplicate — 409 OPERATION_IN_PROGRESS with
	// Retry-After, and the row stays running.
	_, err = e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_workflows
		 (operation_id, operation_type, idempotency_key, request_fingerprint, actor_user_id,
		  state, current_step, retry_deadline_at, lease_owner, leased_until, claim_token)
		 VALUES ($1, 'managed_user.create', $2, $3, $4, 'running', 'created',
		         now() + interval '1 hour', 'worker-x', now() + interval '30 seconds', $5)`,
		uuid.NewString(), key, fp, actor, uuid.NewString())
	require.NoError(t, err)

	status, headers, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key,
		map[string]any{"username": username, "password": "password_123", "roles": []int64{}})
	requireError(t, status, resp, http.StatusConflict, "OPERATION_IN_PROGRESS")
	assert.Equal(t, "1", headers.Get("Retry-After"), "OPERATION_IN_PROGRESS carries the lease Retry-After")

	var state string
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT state FROM admin_workflows WHERE idempotency_key = $1`, key).Scan(&state))
	assert.Equal(t, "running", state, "the in-flight workflow is untouched")
}

func TestWorkflowWrite_AwaitingKeyResumes(t *testing.T) {
	e := newWriteEnv(t, "idem_await")
	key := "awaiting-key-00000000000001"
	actor := e.admin.id
	username := "idem_await_user"
	fp, err := consistency.FingerprintV1("managed_user.create", actor, map[string]any{
		"username":                  username,
		"account":                   consistency.OptString(nil),
		"email":                     consistency.OptString(nil),
		"role_ids":                  consistency.SortedIDs(nil),
		"department_id":             consistency.OptInt(nil),
		"password_change_requested": true,
	})
	require.NoError(t, err)

	// A same-key workflow parked awaiting_client_input (credential never
	// durably committed, deadline still open): the same-key HTTP retry
	// supplies the credential and continues the ORIGINAL workflow — no new
	// side effects are started.
	_, err = e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_workflows
		 (operation_id, operation_type, idempotency_key, request_fingerprint, actor_user_id,
		  state, current_step, retry_deadline_at, client_input_deadline_at)
		 VALUES ($1, 'managed_user.create', $2, $3, $4, 'awaiting_client_input', 'created',
		         now() + interval '1 hour', now() + interval '30 minutes')`,
		uuid.NewString(), key, fp, actor)
	require.NoError(t, err)

	status, _, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key,
		map[string]any{"username": username, "password": "password_123", "roles": []int64{}})
	require.Equal(t, http.StatusOK, status, "awaiting retry continues: %v", resp)
	assert.Equal(t, map[string]any{}, resp["data"])

	// The original workflow converged to succeeded and the user exists.
	var state string
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT state FROM admin_workflows WHERE idempotency_key = $1`, key).Scan(&state))
	assert.Equal(t, "succeeded", state)

	var userCount int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE username = $1`, username).Scan(&userCount))
	assert.Equal(t, int64(1), userCount, "the resumed workflow created the user")
}

// T058 current authorization before idempotency lookup and completed replay
// (contracts/http-api-compatibility.md line 63): authentication and route
// authorization precede idempotency lookup; a completed replay returns the
// stored success only while the actor remains authorized. A revoked actor
// receives the CURRENT 403; a restored actor may replay stored success but
// cannot reopen a workflow terminally rejected due to revocation.
func TestWorkflowWrite_CurrentAuthGatesReplay(t *testing.T) {
	e := newWriteEnv(t, "idem_reauth")
	key := "reauth-key-00000000000001"
	username := "reauth_replay_user"
	body := map[string]any{"username": username, "password": "password_123", "roles": []int64{}}

	status, _, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, body)
	require.Equal(t, http.StatusOK, status, "create: %v", resp)

	// Revoke the admin role: the session stays valid, but route
	// authorization now fails BEFORE the idempotency lookup — the same-key
	// replay receives the current 403, never the stored 200.
	e.revokeAdminRole(t)
	status, _, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, body)
	requireError(t, status, resp, http.StatusForbidden, "AUTH_FORBIDDEN")

	// Restore the grant: the same replay now returns the stored success
	// without duplicating the side effect.
	e.restoreAdminRole(t)
	status, _, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, key, body)
	require.Equal(t, http.StatusOK, status, "restored replay: %v", resp)
	var count int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE username = $1`, username).Scan(&count))
	assert.Equal(t, int64(1), count, "restored replay must not duplicate side effects")

	// A workflow terminally rejected due to revocation is NOT reopened by a
	// restored actor: the same-key replay returns the stored rejection, the
	// workflow stays rejected, and no participant command is issued.
	rejectedKey := "reauth-rejected-00001"
	fp, err := consistency.FingerprintV1("managed_user.create", e.admin.id, map[string]any{
		"username":                  "reauth_rejected_user",
		"account":                   consistency.OptString(nil),
		"email":                     consistency.OptString(nil),
		"role_ids":                  consistency.SortedIDs(nil),
		"department_id":             consistency.OptInt(nil),
		"password_change_requested": true,
	})
	require.NoError(t, err)
	opID := uuid.NewString()
	_, err = e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_workflows
		 (operation_id, operation_type, idempotency_key, request_fingerprint, actor_user_id,
		  state, current_step, retry_deadline_at, result)
		 VALUES ($1, 'managed_user.create', $2, $3, $4, 'rejected', 'created',
		         now() + interval '1 hour', $5::jsonb)`,
		opID, rejectedKey, fp, e.admin.id,
		`{"error":{"kind":"forbidden","message":"forbidden"}}`)
	require.NoError(t, err)

	status, _, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users", e.admin.token, rejectedKey,
		map[string]any{"username": "reauth_rejected_user", "password": "password_123", "roles": []int64{}})
	requireError(t, status, resp, http.StatusForbidden, "AUTH_FORBIDDEN")

	var state string
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT state FROM admin_workflows WHERE idempotency_key = $1`, rejectedKey).Scan(&state))
	assert.Equal(t, "rejected", state, "a revoked-rejected workflow is never reopened")
	var receipts int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_command_receipts WHERE operation_id = $1`, opID).Scan(&receipts))
	assert.Equal(t, int64(0), receipts, "replay of a revoked-rejected workflow issues no commands")
}

func TestWorkflowWrite_DeleteReplayNoDuplicates(t *testing.T) {
	e := newWriteEnv(t, "idem_delete")
	key := "delete-key-000000000000001"
	user := e.register(t, e.prefix+"_victim", "victim_pass_123")

	body := map[string]any{"ids": []int64{user.id}}
	status, _, resp := doJSONKey(t, e.router, http.MethodPost, "/api/v1/users/delete", e.admin.token, key, body)
	require.Equal(t, http.StatusOK, status, "delete: %v", resp)

	// Same-key replay after the unknown outcome returns the stored success
	// and must not produce a second deletion event.
	status, _, resp = doJSONKey(t, e.router, http.MethodPost, "/api/v1/users/delete", e.admin.token, key, body)
	require.Equal(t, http.StatusOK, status, "delete replay: %v", resp)

	var receipts int64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_command_receipts WHERE command_name = 'delete_users'
		 AND operation_id IN (SELECT operation_id FROM admin_workflows WHERE idempotency_key = $1)`, key).Scan(&receipts))
	assert.Equal(t, int64(1), receipts, "replay must not re-issue the IAM deletion")
}
