// Package integration wires the IAM transactional outbox to the Organization
// inbox (contracts/domain-events.md): the envelope v1 codec (this file), the
// dispatcher loop, and the crash-matrix coverage. It only touches owner
// application ports — never IAM/Organization sqlc, pgx, or producer tables.
package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
)

// Envelope v1 contract constants (contracts/domain-events.md §Event catalog).
const (
	EventTypeUserDeleted    = "iam.user.deleted"
	UserDeletedVersion      = 1
	ProducerIAM             = "iam"
	AggregateTypeUser       = "user"
	InboxHandlerUserDeleted = "remove-membership-on-user-deleted"
)

// FieldError is one contract violation within an EnvelopeError.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// EnvelopeError is a contract validation failure with per-field details.
// Match with errors.Is against ErrInvalidEnvelope; a consumer must never
// acknowledge an envelope that fails it (contract rule 6).
type EnvelopeError struct {
	Fields []FieldError
}

// ErrInvalidEnvelope is the sentinel for any envelope v1 contract violation.
var ErrInvalidEnvelope = &EnvelopeError{}

func (e *EnvelopeError) Error() string {
	return fmt.Sprintf("invalid envelope v1: %d contract violation(s)", len(e.Fields))
}

// Is lets errors.Is match every *EnvelopeError against the sentinel.
func (e *EnvelopeError) Is(target error) bool {
	_, ok := target.(*EnvelopeError)
	return ok
}

// UnsupportedVersionError is a known event type carrying an unsupported
// schema version — the non-retryable block path must treat it separately
// from a contract mismatch so observability can tell them apart.
type UnsupportedVersionError struct {
	EventType    string
	EventVersion int
}

// ErrUnsupportedEnvelopeVersion is the sentinel for unsupported versions.
var ErrUnsupportedEnvelopeVersion = &UnsupportedVersionError{}

func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported envelope version %d for event type %s", e.EventVersion, e.EventType)
}

// Is lets errors.Is match every *UnsupportedVersionError against the sentinel.
func (e *UnsupportedVersionError) Is(target error) bool {
	_, ok := target.(*UnsupportedVersionError)
	return ok
}

// Envelope is the wire format (contracts/domain-events.md §Envelope v1).
// Field order is fixed to the contract example so encoded bytes are
// deterministic. Payload values decode as json.Number (exact int64s).
type Envelope struct {
	EventID          string         `json:"event_id"`
	EventType        string         `json:"event_type"`
	EventVersion     int            `json:"event_version"`
	Producer         string         `json:"producer"`
	AggregateType    string         `json:"aggregate_type"`
	AggregateID      string         `json:"aggregate_id"`
	AggregateVersion int64          `json:"aggregate_version"`
	OccurredAt       time.Time      `json:"occurred_at"`
	CorrelationID    string         `json:"correlation_id"`
	Payload          map[string]any `json:"payload"`
}

var (
	eventTypeRe     = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$`)
	versionSuffixRe = regexp.MustCompile(`\.v[0-9]+$`) // type must not carry the version suffix
	correlationRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// ValidCorrelationID reports whether id fits the envelope v1 correlation
// charset (T073): the HTTP request ID shares this exact language (see
// internal/middleware/request_id.go), so any accepted request ID can be
// placed into an envelope untouched. The charset is bounded — correlation is
// opaque non-credential context, never derived from tokens.
func ValidCorrelationID(id string) bool {
	return correlationRe.MatchString(id)
}

// EncodeEnvelope renders a claimed outbox event as envelope v1 JSON. The
// claimed fields are validated first — a malformed producer row never
// becomes a wire envelope. OccurredAt is serialized as RFC3339 UTC.
func EncodeEnvelope(ev iam.ClaimedEvent) ([]byte, error) {
	env := Envelope{
		EventID:          ev.EventID,
		EventType:        ev.EventType,
		EventVersion:     ev.EventVersion,
		Producer:         ev.Producer,
		AggregateType:    ev.AggregateType,
		AggregateID:      ev.AggregateID,
		AggregateVersion: ev.AggregateVersion,
		OccurredAt:       ev.OccurredAt,
		CorrelationID:    ev.CorrelationID,
		Payload:          ev.Payload,
	}
	if err := validateEnvelope(env); err != nil {
		return nil, err
	}
	return json.Marshal(env)
}

// DecodeEnvelope parses envelope v1 JSON and validates every required field
// (contracts/domain-events.md §Envelope v1). Unknown top-level fields are
// ignored — the compatible-addition rule. Payload numbers decode exactly
// (json.Number); malformed JSON is an EnvelopeError, never a crash.
func DecodeEnvelope(data []byte) (Envelope, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var env Envelope
	if err := dec.Decode(&env); err != nil {
		return Envelope{}, envelopeMalformed(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Envelope{}, envelopeMalformed(fmt.Errorf("trailing data after envelope"))
	}
	if err := validateEnvelope(env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

func envelopeMalformed(err error) error {
	return &EnvelopeError{Fields: []FieldError{{Field: "envelope", Code: "malformed_json", Message: err.Error()}}}
}

// validateEnvelope enforces the envelope v1 required-field rules shared by
// every event type. Aggregate versions are positive — the first aggregate
// version is 1, so a missing/zero version is never valid.
func validateEnvelope(env Envelope) error {
	var fields []FieldError
	if err := validateUUID(env.EventID); err != nil {
		fields = append(fields, FieldError{Field: "event_id", Code: "invalid_uuid", Message: "must be a UUID"})
	}
	switch {
	case !eventTypeRe.MatchString(env.EventType):
		fields = append(fields, FieldError{Field: "event_type", Code: "invalid_format", Message: "stable lowercase dotted name"})
	case versionSuffixRe.MatchString(env.EventType):
		fields = append(fields, FieldError{Field: "event_type", Code: "version_suffix", Message: "type must not carry a .vN version suffix"})
	}
	if env.EventVersion <= 0 {
		fields = append(fields, FieldError{Field: "event_version", Code: "invalid", Message: "must be a positive integer"})
	}
	if env.Producer == "" {
		fields = append(fields, FieldError{Field: "producer", Code: "required", Message: "stable owner identifier"})
	}
	if env.AggregateType == "" {
		fields = append(fields, FieldError{Field: "aggregate_type", Code: "required", Message: "stable aggregate category"})
	}
	if env.AggregateID == "" {
		fields = append(fields, FieldError{Field: "aggregate_id", Code: "required", Message: "string representation"})
	}
	if env.AggregateVersion <= 0 {
		fields = append(fields, FieldError{Field: "aggregate_version", Code: "invalid", Message: "must be a positive integer"})
	}
	if env.OccurredAt.IsZero() {
		fields = append(fields, FieldError{Field: "occurred_at", Code: "required", Message: "domain change time"})
	} else if _, offset := env.OccurredAt.Zone(); offset != 0 {
		fields = append(fields, FieldError{Field: "occurred_at", Code: "invalid", Message: "must be UTC"})
	}
	if !correlationRe.MatchString(env.CorrelationID) {
		fields = append(fields, FieldError{Field: "correlation_id", Code: "invalid", Message: "bounded non-token identifier"})
	}
	if env.Payload == nil {
		fields = append(fields, FieldError{Field: "payload", Code: "required", Message: "JSON object"})
	}
	if len(fields) > 0 {
		return &EnvelopeError{Fields: fields}
	}
	return nil
}

// DecodeUserDeletedV1 validates a decoded envelope against the
// iam.user.deleted v1 consumer contract and maps it to the Organization
// inbox input (contracts/domain-events.md §iam.user.deleted version 1).
// A known type with an unsupported version is UnsupportedVersionError;
// everything else that fails is an EnvelopeError contract mismatch. Both
// paths must block (never retry hot) and must not write an inbox row.
func DecodeUserDeletedV1(env Envelope) (organization.IAMUserDeletedEvent, error) {
	if env.EventType == EventTypeUserDeleted && env.EventVersion != UserDeletedVersion {
		if env.EventVersion > UserDeletedVersion {
			return organization.IAMUserDeletedEvent{}, &UnsupportedVersionError{EventType: env.EventType, EventVersion: env.EventVersion}
		}
		return organization.IAMUserDeletedEvent{}, &EnvelopeError{Fields: []FieldError{{Field: "event_version", Code: "invalid", Message: "must be a positive integer"}}}
	}

	var fields []FieldError
	if env.Producer != ProducerIAM {
		fields = append(fields, FieldError{Field: "producer", Code: "invalid", Message: "must be iam"})
	}
	if env.EventType != EventTypeUserDeleted {
		fields = append(fields, FieldError{Field: "event_type", Code: "invalid", Message: "must be iam.user.deleted"})
	}
	if env.AggregateType != AggregateTypeUser {
		fields = append(fields, FieldError{Field: "aggregate_type", Code: "invalid", Message: "must be user"})
	}
	if env.AggregateVersion <= 0 { // defensive; the generic codec already requires it
		fields = append(fields, FieldError{Field: "aggregate_version", Code: "invalid", Message: "must be positive"})
	}

	// Payload must be exactly {user_id: positive int64} — no additional keys.
	userID, ok := asInt64(env.Payload["user_id"])
	switch {
	case len(env.Payload) != 1:
		fields = append(fields, FieldError{Field: "payload", Code: "invalid", Message: "must contain only user_id"})
	case !ok || userID <= 0:
		fields = append(fields, FieldError{Field: "payload", Code: "invalid", Message: "user_id must be a positive integer"})
	case env.AggregateID != strconv.FormatInt(userID, 10):
		fields = append(fields, FieldError{Field: "aggregate_id", Code: "invalid", Message: "must match user_id"})
	}
	if len(fields) > 0 {
		return organization.IAMUserDeletedEvent{}, &EnvelopeError{Fields: fields}
	}

	return organization.IAMUserDeletedEvent{
		EventID:          env.EventID,
		EventType:        env.EventType,
		EventVersion:     env.EventVersion,
		Producer:         env.Producer,
		AggregateType:    env.AggregateType,
		AggregateID:      env.AggregateID,
		AggregateVersion: env.AggregateVersion,
		UserID:           userID,
	}, nil
}

// --- helpers -----------------------------------------------------------------

// validateUUID rejects anything that is not a canonical UUID text.
func validateUUID(s string) error {
	if len(s) != 36 {
		return errors.New("not a UUID")
	}
	// canonical 8-4-4-4-12 hex groups (no braced/urn variants on the wire)
	if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return errors.New("not a UUID")
	}
	for _, r := range s {
		if r == '-' {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return errors.New("not a UUID")
		}
	}
	return nil
}

// asInt64 extracts an exact int64 from a decoded payload value. Decoded
// payloads carry json.Number (json.Decoder.UseNumber) so integers survive
// beyond float64 precision; hand-built envelopes may carry float64.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		if n != math.Trunc(n) || n < -9.223372036854776e18 || n >= 9.223372036854776e18 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}
