// Package iam owns identity, credentials, sessions, roles, permissions and
// authorization. Application models are transport-neutral: no Gin, HTTP
// status, pgx, pgtype or sqlc-generated types appear here.
package iam

import "time"

// LifecycleState is the identity aggregate lifecycle (contracts/iam-application.md).
type LifecycleState string

const (
	LifecycleProvisioning LifecycleState = "provisioning"
	LifecycleActive       LifecycleState = "active"
	LifecycleDisabled     LifecycleState = "disabled"
)

// Principal is produced only after successful session authentication.
// The raw token is consumed by the authentication adapter and never appears here.
type Principal struct {
	UserID    int64
	Username  string
	SessionID string // internal stable session reference, not the raw token
}

// IdentityProfile is the identity aggregate without Organization fields.
type IdentityProfile struct {
	ID             int64
	Username       string
	Account        *string
	Email          *string
	LifecycleState LifecycleState
	Version        int64 // monotonic IAM aggregate version
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// RoleSummary is the public role projection.
type RoleSummary struct {
	ID        int64
	Name      string
	Code      string
	IsBuiltin bool
	CreatedAt time.Time
}

// AuthorizationProfile is the current IAM grant projection.
// EffectivePermissions is always non-nil, sorted and unique; empty is [].
type AuthorizationProfile struct {
	Roles                []RoleSummary
	EffectivePermissions []string
}

// ManagedIdentity is IdentityProfile plus role projections for the BFF user
// list and edit use cases. It never contains a password hash or session token.
type ManagedIdentity struct {
	IdentityProfile
	Roles []RoleSummary
}

// ManagedIdentityPage is a paginated managed-user result.
type ManagedIdentityPage struct {
	Items []ManagedIdentity
	Total int64
}

// AuthSession carries the raw opaque token exactly once at issue time;
// storage keeps only the token hash.
type AuthSession struct {
	Token     string
	TokenType string
	ExpiresIn time.Duration
	User      Principal
}

// OperationContext is carried by every workflow mutation.
// OperationID is always present; IdempotencyKey is the optional public key.
type OperationContext struct {
	OperationID    string
	IdempotencyKey *string
	ActorUserID    int64
	CorrelationID  string
}

// CreateUserResult is the provisioning-command outcome.
type CreateUserResult struct {
	UserID           int64
	ResultingVersion int64
}

// DeleteTarget is one expected-version deletion subject.
type DeleteTarget struct {
	UserID          int64
	ExpectedVersion int64
}

// BatchDeleteResultItem is one tombstone version per deleted subject.
type BatchDeleteResultItem struct {
	UserID           int64
	TombstoneVersion int64
}

// GrantAdminResult is the admin bootstrap outcome.
type GrantAdminResult struct {
	UserID           int64
	ResultingVersion int64
}

// CommandStatus classifies a stored command receipt. The values match the
// iam_command_receipts CHECK constraint ('succeeded', 'rejected').
type CommandStatus string

const (
	CommandSucceeded CommandStatus = "succeeded"
	CommandRejected  CommandStatus = "rejected"
)

// IAMCommandReceipt is the owner-local evidence of a committed workflow side
// effect. SafeResult is transport-neutral and contains no secrets.
type IAMCommandReceipt struct {
	OperationID        string
	CommandName        string
	RequestFingerprint string
	Status             CommandStatus
	SubjectID          *int64
	ResultingVersion   *int64
	SafeResult         map[string]any
	ErrorCode          *string
}

// ClaimedEvent is one leased outbox row handed to a dispatcher consumer. The
// envelope identity fields (event_id/type/version/producer/aggregate/occurred/
// correlation) come straight from the durable row, so the dispatcher never
// re-derives or regenerates them on retry.
type ClaimedEvent struct {
	EventID           string
	EventType         string
	EventVersion      int
	Producer          string
	AggregateType     string
	AggregateID       string
	AggregateVersion  int64
	OccurredAt        time.Time
	CorrelationID     string
	Payload           map[string]any // safe IDs only, never credentials
	ClaimToken        string
	LeaseOwner        string
	LeasedUntil       time.Time
	AttemptCount      int
	TotalAttemptCount int
}

// OutboxDeliveryStatus is the outcome of a failed delivery attempt.
type OutboxDeliveryStatus string

const (
	OutboxPending OutboxDeliveryStatus = "pending"
	OutboxBlocked OutboxDeliveryStatus = "blocked(delivery_exhausted)"
)

// OutboxBacklog is the safe operational backlog summary (contracts/
// domain-events.md §Failure and reconciliation Required observability).
// Cumulative counters (LeaseRecoveryCount, StaleClaimRejectionCount) are
// maintained by the delivery adapter and never affect delivery semantics.
type OutboxBacklog struct {
	PendingCount    int64
	LeasedCount     int64
	BlockedCount    int64
	PublishedCount  int64
	OldestPendingAt *time.Time
	// OldestLeasedUntil is the earliest live lease expiry (nil when nothing
	// is leased); an age past the lease duration flags a stuck worker.
	OldestLeasedUntil *time.Time
	// LeaseRecoveryCount counts expired-lease reclaims (rule 9: a crashed
	// worker's lease is recovered by another claimer).
	LeaseRecoveryCount int64
	// StaleClaimRejectionCount counts rejected acks/failures/blocks that
	// carried an old claim token (rule 8).
	StaleClaimRejectionCount int64
}

// TrustedRecoveryContext is created only by an authenticated Platform
// operations adapter; IAM rejects arbitrary caller text.
type TrustedRecoveryContext struct {
	PrincipalID         int64
	AuthorizationSource string
	ApprovalID          string
	CorrelationID       string
	RequestID           string
}

// EvidenceCleanupEvaluation is the conservative dry-run result.
type EvidenceCleanupEvaluation struct {
	EligibleCounts          map[string]int64
	BlockingUnresolvedCount int64
	OldestReplayableAt      *time.Time
}

// Store-facing records ------------------------------------------------------
// Rows exchanged with the Store port (driven side). Application-neutral: no
// pgx/pgtype/sqlc-generated types appear here (boundary rule).

// UserRecord is the identity row as read/written by the Store. PasswordHash
// is consumed for authentication inside the service and never leaves it (no
// event payload or log may carry it).
type UserRecord struct {
	ID             int64
	Username       string
	PasswordHash   string
	Account        *string
	Email          *string
	LifecycleState LifecycleState
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SessionRecord is a session row joined with its owner's current state; the
// Authenticate op needs both for the validity and active-user checks.
type SessionRecord struct {
	ID             int64
	TokenHash      string
	UserID         int64
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RevokedAt      *time.Time
	Username       string
	LifecycleState LifecycleState
}

// RoleRecord is a role row as read/written by the Store. Built-in
// classification is service policy (derived from the code), never stored.
type RoleRecord struct {
	ID        int64
	Name      string
	Code      string
	CreatedAt time.Time
}

// UserRoleAssignment is one user<->role link used to aggregate role
// projections for user batches without per-row queries.
type UserRoleAssignment struct {
	UserID int64
	Role   RoleRecord
}

// OutboxEventRecord is one produced domain event handed to the Store for
// enqueue; status/availability default to pending/immediately-available in the
// SQL. Payload carries only consumer-required safe data — never credentials.
// The event commits in the same IAM transaction as its side effect.
type OutboxEventRecord struct {
	EventID          string
	EventType        string
	EventVersion     int
	Producer         string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Payload          map[string]any
	CorrelationID    string
	OccurredAt       time.Time
}
