package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// Contract-first envelope v1 codec suite (contracts/domain-events.md §Envelope
// v1 + §iam.user.deleted version 1). Pure function tests — no database.

// contractExample is the domain-events.md §Envelope v1 sample verbatim.
const contractExample = `{
  "event_id": "1d2b1228-6c17-4f21-b4ce-38d88e5c6f11",
  "event_type": "iam.user.deleted",
  "event_version": 1,
  "producer": "iam",
  "aggregate_type": "user",
  "aggregate_id": "42",
  "aggregate_version": 7,
  "occurred_at": "2026-08-10T12:00:00Z",
  "correlation_id": "request-or-workflow-id",
  "payload": {
    "user_id": 42
  }
}`

func contractClaimed() iam.ClaimedEvent {
	return iam.ClaimedEvent{
		EventID:          "1d2b1228-6c17-4f21-b4ce-38d88e5c6f11",
		EventType:        "iam.user.deleted",
		EventVersion:     1,
		Producer:         "iam",
		AggregateType:    "user",
		AggregateID:      "42",
		AggregateVersion: 7,
		OccurredAt:       time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		CorrelationID:    "request-or-workflow-id",
		Payload:          map[string]any{"user_id": float64(42)},
	}
}

func contractEnvelope() Envelope {
	return Envelope{
		EventID:          "1d2b1228-6c17-4f21-b4ce-38d88e5c6f11",
		EventType:        "iam.user.deleted",
		EventVersion:     1,
		Producer:         "iam",
		AggregateType:    "user",
		AggregateID:      "42",
		AggregateVersion: 7,
		OccurredAt:       time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		CorrelationID:    "request-or-workflow-id",
		Payload:          map[string]any{"user_id": json.Number("42")},
	}
}

// envelopeJSON marshals a map so tests can build the wire form of any shape.
func envelopeJSON(overrides map[string]any) []byte {
	m := map[string]any{
		"event_id":          "1d2b1228-6c17-4f21-b4ce-38d88e5c6f11",
		"event_type":        "iam.user.deleted",
		"event_version":     1,
		"producer":          "iam",
		"aggregate_type":    "user",
		"aggregate_id":      "42",
		"aggregate_version": 7,
		"occurred_at":       "2026-08-10T12:00:00Z",
		"correlation_id":    "request-or-workflow-id",
		"payload":           map[string]any{"user_id": 42},
	}
	for k, v := range overrides {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// wantEnvelopeError asserts the error is the invalid-envelope sentinel.
func wantEnvelopeError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("want EnvelopeError (invalid envelope), got %v", err)
	}
}

// --- EncodeEnvelope ---------------------------------------------------------

func TestEncodeEnvelope_ContractShape(t *testing.T) {
	got, err := EncodeEnvelope(contractClaimed())
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	// Field order and encoding must match the contract example exactly
	// (occurred_at RFC3339 UTC, payload user_id as number).
	want := `{"event_id":"1d2b1228-6c17-4f21-b4ce-38d88e5c6f11","event_type":"iam.user.deleted","event_version":1,"producer":"iam","aggregate_type":"user","aggregate_id":"42","aggregate_version":7,"occurred_at":"2026-08-10T12:00:00Z","correlation_id":"request-or-workflow-id","payload":{"user_id":42}}`
	if string(got) != want {
		t.Fatalf("envelope mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestEncodeEnvelope_RejectsMalformedClaimed(t *testing.T) {
	valid := contractClaimed()
	cases := []struct {
		name string
		mut  func(*iam.ClaimedEvent)
	}{
		{"non-uuid event id", func(e *iam.ClaimedEvent) { e.EventID = "not-a-uuid" }},
		{"empty event type", func(e *iam.ClaimedEvent) { e.EventType = "" }},
		{"zero event version", func(e *iam.ClaimedEvent) { e.EventVersion = 0 }},
		{"empty producer", func(e *iam.ClaimedEvent) { e.Producer = "" }},
		{"empty aggregate type", func(e *iam.ClaimedEvent) { e.AggregateType = "" }},
		{"empty aggregate id", func(e *iam.ClaimedEvent) { e.AggregateID = "" }},
		{"zero aggregate version", func(e *iam.ClaimedEvent) { e.AggregateVersion = 0 }},
		{"zero occurred at", func(e *iam.ClaimedEvent) { e.OccurredAt = time.Time{} }},
		{"empty correlation id", func(e *iam.ClaimedEvent) { e.CorrelationID = "" }},
		{"nil payload", func(e *iam.ClaimedEvent) { e.Payload = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := valid
			tc.mut(&ev)
			_, err := EncodeEnvelope(ev)
			wantEnvelopeError(t, err)
		})
	}
}

// --- DecodeEnvelope ---------------------------------------------------------

func TestDecodeEnvelope_ContractExample(t *testing.T) {
	env, err := DecodeEnvelope([]byte(contractExample))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	want := contractEnvelope()
	if env.EventID != want.EventID || env.EventType != want.EventType || env.EventVersion != want.EventVersion ||
		env.Producer != want.Producer || env.AggregateType != want.AggregateType || env.AggregateID != want.AggregateID ||
		env.AggregateVersion != want.AggregateVersion || env.CorrelationID != want.CorrelationID ||
		!env.OccurredAt.Equal(want.OccurredAt) {
		t.Fatalf("decoded envelope mismatch: got %+v", env)
	}
	if userID, ok := asInt64(env.Payload["user_id"]); !ok || userID != 42 {
		t.Fatalf("payload user_id = %v (%v), want 42", env.Payload["user_id"], userID)
	}
}

func TestDecodeEnvelope_IgnoresUnknownEnvelopeFields(t *testing.T) {
	// Compatible-addition rule: old consumers ignore unknown fields.
	data := envelopeJSON(map[string]any{"future_field": map[string]any{"a": 1}})
	if _, err := DecodeEnvelope(data); err != nil {
		t.Fatalf("unknown top-level field must be ignored, got %v", err)
	}
}

func TestDecodeEnvelope_RejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value any // nil removes the key
	}{
		{"event_id", "event_id", nil},
		{"event_type", "event_type", nil},
		{"event_version", "event_version", nil},
		{"producer", "producer", nil},
		{"aggregate_type", "aggregate_type", nil},
		{"aggregate_id", "aggregate_id", nil},
		{"aggregate_version", "aggregate_version", nil},
		{"occurred_at", "occurred_at", nil},
		{"correlation_id", "correlation_id", nil},
		{"payload", "payload", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeEnvelope(envelopeJSON(map[string]any{tc.field: tc.value}))
			wantEnvelopeError(t, err)
			var envErr *EnvelopeError
			if !errors.As(err, &envErr) {
				t.Fatalf("want *EnvelopeError, got %T", err)
			}
			for _, f := range envErr.Fields {
				if f.Field == tc.field {
					return
				}
			}
			t.Fatalf("no field error for %q in %+v", tc.field, envErr.Fields)
		})
	}
}

func TestDecodeEnvelope_RejectsNonUUIDEventID(t *testing.T) {
	_, err := DecodeEnvelope(envelopeJSON(map[string]any{"event_id": "42"}))
	wantEnvelopeError(t, err)
}

func TestDecodeEnvelope_RejectsTypeWithVersionSuffix(t *testing.T) {
	for _, eventType := range []string{"iam.user.deleted.v1", "iam.user.deleted.v2", "iam.v1", "iam.user.deleted.v10"} {
		t.Run(eventType, func(t *testing.T) {
			_, err := DecodeEnvelope(envelopeJSON(map[string]any{"event_type": eventType}))
			wantEnvelopeError(t, err)
		})
	}
}

func TestDecodeEnvelope_RejectsMalformedEventType(t *testing.T) {
	for _, eventType := range []string{"", "Iam.user.deleted", "iam_user_deleted", "iam", "iam..deleted"} {
		t.Run(eventType, func(t *testing.T) {
			_, err := DecodeEnvelope(envelopeJSON(map[string]any{"event_type": eventType}))
			wantEnvelopeError(t, err)
		})
	}
}

func TestDecodeEnvelope_RejectsNonPositiveEventVersion(t *testing.T) {
	for _, v := range []int{0, -1} {
		t.Run(string(rune(v)), func(t *testing.T) {
			_, err := DecodeEnvelope(envelopeJSON(map[string]any{"event_version": v}))
			wantEnvelopeError(t, err)
		})
	}
}

func TestDecodeEnvelope_RejectsNonUTCOccurredAt(t *testing.T) {
	_, err := DecodeEnvelope(envelopeJSON(map[string]any{"occurred_at": "2026-08-10T12:00:00+08:00"}))
	wantEnvelopeError(t, err)
}

func TestDecodeEnvelope_RejectsMalformedOccurredAt(t *testing.T) {
	_, err := DecodeEnvelope(envelopeJSON(map[string]any{"occurred_at": "not-a-time"}))
	wantEnvelopeError(t, err)
}

func TestDecodeEnvelope_RejectsNonObjectPayload(t *testing.T) {
	for _, payload := range []any{nil, []int{1}, "str", 42} {
		t.Run("payload", func(t *testing.T) {
			_, err := DecodeEnvelope(envelopeJSON(map[string]any{"payload": payload}))
			wantEnvelopeError(t, err)
		})
	}
}

func TestDecodeEnvelope_RejectsCorrelationID(t *testing.T) {
	long := strings.Repeat("a", 129)
	for _, correlationID := range []string{"", "has space", "汉字", long} {
		t.Run(correlationID, func(t *testing.T) {
			_, err := DecodeEnvelope(envelopeJSON(map[string]any{"correlation_id": correlationID}))
			wantEnvelopeError(t, err)
		})
	}
}

func TestDecodeEnvelope_RejectsTrailingGarbage(t *testing.T) {
	_, err := DecodeEnvelope([]byte(contractExample + ` garbage`))
	wantEnvelopeError(t, err)
}

// --- DecodeUserDeletedV1 ----------------------------------------------------

func TestDecodeUserDeletedV1_Valid(t *testing.T) {
	env, err := DecodeEnvelope([]byte(contractExample))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	got, err := DecodeUserDeletedV1(env)
	if err != nil {
		t.Fatalf("DecodeUserDeletedV1: %v", err)
	}
	want := organization.IAMUserDeletedEvent{
		EventID:          "1d2b1228-6c17-4f21-b4ce-38d88e5c6f11",
		EventType:        "iam.user.deleted",
		EventVersion:     1,
		Producer:         "iam",
		AggregateType:    "user",
		AggregateID:      "42",
		AggregateVersion: 7,
		UserID:           42,
	}
	if got != want {
		t.Fatalf("decoded event mismatch: got %+v want %+v", got, want)
	}
}

func TestDecodeUserDeletedV1_UnsupportedVersion(t *testing.T) {
	for _, v := range []int{2, 3} {
		t.Run("v", func(t *testing.T) {
			env, err := DecodeEnvelope(envelopeJSON(map[string]any{"event_version": v}))
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			_, err = DecodeUserDeletedV1(env)
			if !errors.Is(err, ErrUnsupportedEnvelopeVersion) {
				t.Fatalf("want unsupported version sentinel, got %v", err)
			}
		})
	}
}

func TestDecodeUserDeletedV1_ContractMismatch(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value any
	}{
		{"producer", "producer", "rbac"},
		{"event type", "event_type", "iam.user.renamed"},
		{"aggregate type", "aggregate_type", "group"},
		{"aggregate id mismatch", "aggregate_id", "43"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := DecodeEnvelope(envelopeJSON(map[string]any{tc.field: tc.value}))
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			_, err = DecodeUserDeletedV1(env)
			wantEnvelopeError(t, err)
		})
	}
}

func TestDecodeUserDeletedV1_RejectsZeroAggregateVersion(t *testing.T) {
	// Defensive: the generic codec already requires aggregate_version > 0,
	// so this only exercises the specialized guard on a hand-built envelope.
	env := contractEnvelope()
	env.AggregateVersion = 0
	_, err := DecodeUserDeletedV1(env)
	wantEnvelopeError(t, err)
}

func TestDecodeUserDeletedV1_PayloadOnlyUserID(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"extra key", map[string]any{"user_id": json.Number("42"), "email": "x@example.com"}},
		{"empty payload", map[string]any{}},
		{"missing user id", map[string]any{"other": 1}},
		{"fractional user id", map[string]any{"user_id": json.Number("42.5")}},
		{"zero user id", map[string]any{"user_id": json.Number("0")}},
		{"negative user id", map[string]any{"user_id": json.Number("-3")}},
		{"overflowing user id", map[string]any{"user_id": json.Number("9223372036854775808")}},
		{"string user id", map[string]any{"user_id": "42"}},
		{"float user id", map[string]any{"user_id": 42.5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := contractEnvelope()
			env.Payload = tc.payload
			_, err := DecodeUserDeletedV1(env)
			wantEnvelopeError(t, err)
		})
	}
}

// --- roundtrip --------------------------------------------------------------

func TestRoundtrip_EncodeDecodeClaimedEvent(t *testing.T) {
	ev := contractClaimed()
	ev.OccurredAt = time.Date(2026, 8, 10, 12, 0, 0, 123456789, time.UTC) // sub-second precision must survive
	data, err := EncodeEnvelope(ev)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	env, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.EventID != ev.EventID || env.EventType != ev.EventType || env.EventVersion != ev.EventVersion ||
		env.Producer != ev.Producer || env.AggregateType != ev.AggregateType || env.AggregateID != ev.AggregateID ||
		env.AggregateVersion != ev.AggregateVersion || env.CorrelationID != ev.CorrelationID {
		t.Fatalf("roundtrip mismatch: got %+v", env)
	}
	if !env.OccurredAt.Equal(ev.OccurredAt) {
		t.Fatalf("occurred_at precision lost: got %v want %v", env.OccurredAt, ev.OccurredAt)
	}
	if userID, ok := asInt64(env.Payload["user_id"]); !ok || userID != 42 {
		t.Fatalf("payload user_id = %v (%v), want 42", env.Payload["user_id"], userID)
	}
}
