package iam

import (
	"context"
	"time"
)

// Application ports (contracts/iam-application.md). Small compile-time
// interfaces keep adapters swappable; no adapter type leaks through here.

// AuthService handles the session lifecycle. Raw tokens are consumed here and
// never returned except at issue time inside AuthSession.
type AuthService interface {
	Register(ctx context.Context, username, password string, requestCtx OperationContext) (AuthSession, error)
	Login(ctx context.Context, username, password string, requestCtx OperationContext) (AuthSession, error)
	RevokeSession(ctx context.Context, rawToken string, requestCtx OperationContext) error
	Authenticate(ctx context.Context, rawToken string, requestCtx OperationContext) (Principal, error)
}

// IdentityService reads identity and authorization projections.
type IdentityService interface {
	GetIdentity(ctx context.Context, userID int64) (IdentityProfile, error)
	GetAuthorizationProfile(ctx context.Context, userID int64) (AuthorizationProfile, error)
	// HasPermission queries current grants on every invocation; unknown
	// permission codes return false and never grant access.
	HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error)
	// BatchGetManagedIdentities filters and paginates inside IAM. A nil
	// candidate list means all normal managed users; an empty one yields an
	// empty result without query expansion. Provisioning users are excluded.
	BatchGetManagedIdentities(ctx context.Context, candidateUserIDs []int64,
		usernameFilter, accountFilter *string, pageIndex, pageSize int) (ManagedIdentityPage, error)
}

// RoleService owns role read and write operations.
type RoleService interface {
	ListRoles(ctx context.Context) ([]RoleSummary, error)
	// ValidateRoleIDs validates the entire set; any missing role returns
	// RoleNotFound with no writes. Duplicate input IDs do not duplicate assignments.
	ValidateRoleIDs(ctx context.Context, ids []int64) ([]RoleSummary, error)
	SaveRole(ctx context.Context, requestCtx OperationContext, id *int64, name, code string) (int64, error)
	// DeleteRoles rejects the whole batch on any missing/built-in/referenced role.
	DeleteRoles(ctx context.Context, requestCtx OperationContext, ids []int64) error
}

// ManagedUserService issues workflow commands; every mutation stores a
// command receipt atomically with its side effect.
type ManagedUserService interface {
	CreateProvisioningUser(ctx context.Context, requestCtx OperationContext,
		username string, account, email *string, password string, roleIDs []int64) (CreateUserResult, error)
	// ActivateUser only accepts provisioning -> active with expected-version CAS.
	ActivateUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64) (int64, error)
	DisableUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64, reasonCode string) (int64, error)
	// UpdateManagedUser expects users.version == expectedVersion, then increments.
	UpdateManagedUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64,
		username string, account, email *string, password *string, roleIDs []int64) (int64, error)
	// DeleteUsers is authoritative IAM-local batch deletion with one outbox
	// event per user; any invalid/stale target rejects the entire batch.
	DeleteUsers(ctx context.Context, requestCtx OperationContext, targets []DeleteTarget) ([]BatchDeleteResultItem, error)
	// CompensateProvisioningUser deletes a still-provisioning workflow subject.
	CompensateProvisioningUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64) error
}

// ReceiptResolver resolves a previously committed command after timeout/crash.
// Absent receipt means no committed side effect is proven — never fabricated.
type ReceiptResolver interface {
	ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*IAMCommandReceipt, error)
}

// AdminBootstrap grants an existing registered user the admin or super_admin
// built-in role. It never accepts or creates passwords.
type AdminBootstrap interface {
	GrantBuiltInAdminRole(ctx context.Context, username, roleCode string) (GrantAdminResult, error)
}

// OutboxService is the only delivery path for IAM domain events. Claim
// transactions persist leased state before the consumer call; ack/failure/
// block are claim-token CAS (stale tokens are rejected, contracts/domain-events.md
// rule 8). Each delivery epoch auto-blocks as delivery_exhausted after the 20th
// failed claim or epoch_started_at+24h — the ceiling is evaluated atomically at
// claim time (no lease is handed out) and at failure-recording time (exactly
// one transition: pending/backoff or blocked). Every fresh claim/reclaim counts
// one attempt, so a worker crash after claim still consumes the budget.
type OutboxService interface {
	ClaimOutbox(ctx context.Context, batchSize int, leaseOwner string, leaseDuration time.Duration) ([]ClaimedEvent, error)
	AckOutbox(ctx context.Context, eventID, claimToken string) error
	RecordOutboxFailure(ctx context.Context, eventID, claimToken, safeErrorCode string, nextAvailableAt time.Time) (OutboxDeliveryStatus, error)
	BlockOutbox(ctx context.Context, eventID, claimToken, safeReasonCode string) error
	RequeueBlockedOutbox(ctx context.Context, eventID string, trustedCtx TrustedRecoveryContext, approvedReasonCode string, availableAt time.Time) (int64, error)
	GetOutboxBacklog(ctx context.Context) (OutboxBacklog, error)
}

// EvidenceCleanupService is the conservative owner-local purge port.
type EvidenceCleanupService interface {
	EvaluateEvidenceCleanup(ctx context.Context, cutoff time.Time) (EvidenceCleanupEvaluation, error)
	PurgeEligibleEvidence(ctx context.Context, cutoff time.Time, trustedCtx TrustedRecoveryContext) (map[string]int64, error)
}

// Store is the IAM data-access port (driven side). The PostgreSQL adapter
// (internal/iam/postgres) implements it against the IAM-owned sqlc package;
// the service never touches pgx/sqlc/migrations (boundary rule).
//
// RunInTx executes fn on a Store bound to one IAM-local transaction (commit
// on nil error, rollback otherwise), keeping multi-write operations atomic —
// the transaction may only touch IAM tables.
//
// Absence is signalled with the domain errors: ErrUserNotFound for user
// lookups, ErrRoleNotFound for role lookups, ErrInvalidToken for session
// lookups. Unique violations surface as ErrUsernameTaken (users.username) and
// ErrNameTaken (roles.name / roles.code).
type Store interface {
	RunInTx(ctx context.Context, fn func(Store) error) error

	// users
	GetUserByUsername(ctx context.Context, username string) (UserRecord, error)
	GetUserByID(ctx context.Context, id int64) (UserRecord, error)
	CreateUser(ctx context.Context, username, passwordHash string) (UserRecord, error)
	// CreateProvisioningUser inserts a provisioning-state identity at version 1
	// with its password hash; the caller commits the receipt in the same tx.
	CreateProvisioningUser(ctx context.Context, username, passwordHash string, account, email *string) (UserRecord, error)
	// ActivateUserCAS transitions provisioning -> active exactly when
	// users.version == expectedVersion, atomically. It reports the resulting
	// version and whether the CAS hit; a miss leaves the row untouched and the
	// service distinguishes the failure reason from the current row.
	ActivateUserCAS(ctx context.Context, userID, expectedVersion int64) (resultingVersion int64, hit bool, err error)
	// UpdateManagedUserCAS gates the profile update on users.version ==
	// expectedVersion and bumps it in the same statement; a miss leaves the row
	// untouched and the service classifies the failure from the current row.
	// The unique username constraint surfaces as ErrUsernameTaken.
	UpdateManagedUserCAS(ctx context.Context, userID, expectedVersion int64, username string, account, email *string) (resultingVersion int64, hit bool, err error)
	// UpdateUserPasswordHash replaces the stored hash; called only inside the
	// transaction whose UpdateManagedUserCAS already locked the row, so it
	// cannot interleave with a concurrent updater.
	UpdateUserPasswordHash(ctx context.Context, userID int64, passwordHash string) error
	// DisableUserCAS transitions active -> disabled exactly when
	// users.version == expectedVersion (re-enable is out of scope); a miss
	// leaves the row untouched and the service classifies the failure reason.
	DisableUserCAS(ctx context.Context, userID, expectedVersion int64) (resultingVersion int64, hit bool, err error)
	// DeleteProvisioningUserCAS deletes exactly a still-provisioning subject at
	// the expected version (CompensateProvisioningUser); sessions and
	// user_roles cascade away. A miss leaves the row untouched.
	DeleteProvisioningUserCAS(ctx context.Context, userID, expectedVersion int64) (deletedVersion int64, hit bool, err error)
	// GetUserByIDForUpdate is the DeleteUsers precheck lookup: each target row
	// is locked until the deletion transaction commits, so validation and the
	// DELETE cannot race a concurrent update or delete. Password hash is never
	// selected (deletion does not touch credentials).
	GetUserByIDForUpdate(ctx context.Context, id int64) (UserRecord, error)
	// DeleteUser removes a user row; sessions and user_roles cascade away
	// (schema-level ON DELETE CASCADE).
	DeleteUser(ctx context.Context, id int64) error
	// GetUserByUsernameForUpdate is the admin bootstrap's locked lookup: the
	// row lock is held until the transaction commits so the version bump in
	// GrantBuiltInAdminRole cannot race a concurrent grant.
	GetUserByUsernameForUpdate(ctx context.Context, username string) (UserRecord, error)
	// BumpUserVersion increments users.version; called only inside a
	// transaction that holds the locked row (see above).
	BumpUserVersion(ctx context.Context, userID int64) error

	// sessions
	CreateSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) (int64, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (SessionRecord, error)
	RevokeSessionByTokenHash(ctx context.Context, tokenHash string) error
	// RevokeSessionsByUserID revokes every live session of the subject
	// (DisableUser runs it inside the lifecycle transaction).
	RevokeSessionsByUserID(ctx context.Context, userID int64) error
	TouchSession(ctx context.Context, tokenHash string, expiresAt time.Time) error

	// roles
	GetRoleByCode(ctx context.Context, code string) (RoleRecord, error)
	GetRoleByID(ctx context.Context, id int64) (RoleRecord, error)
	ListRoles(ctx context.Context) ([]RoleRecord, error)
	CreateRole(ctx context.Context, name, code string) (RoleRecord, error)
	UpdateRole(ctx context.Context, id int64, name, code string) (RoleRecord, error)
	DeleteRole(ctx context.Context, id int64) error
	CountUserRolesByRoleID(ctx context.Context, roleID int64) (int64, error)

	// user<->role links
	InsertUserRole(ctx context.Context, userID, roleID int64) error
	DeleteUserRolesByUserID(ctx context.Context, userID int64) error
	ListRolesByUserID(ctx context.Context, userID int64) ([]RoleRecord, error)

	// command receipts (T044): durable proof committed in the same transaction
	// as the command side effect. GetCommandReceipt returns (nil, nil) when no
	// evidence exists — absence never fabricates success or failure.
	SaveCommandReceipt(ctx context.Context, receipt IAMCommandReceipt) error
	GetCommandReceipt(ctx context.Context, operationID, commandName string) (*IAMCommandReceipt, error)

	// outbox (producer side, T048): enqueue a fresh pending domain event in
	// the caller's transaction; rollback removes the event too.
	EnqueueOutboxEvent(ctx context.Context, event OutboxEventRecord) error

	// authorization
	ListEffectivePermissionsByUserID(ctx context.Context, userID int64) ([]string, error)
	HasPermissionByUserID(ctx context.Context, userID int64, permissionCode string) (bool, error)

	// managed-user listing (BatchGetManagedIdentities). A nil candidateIDs
	// means all normal managed users (provisioning excluded); the service
	// short-circuits an empty candidate list before reaching the store.
	ListManagedUsers(ctx context.Context, candidateIDs []int64, usernameFilter, accountFilter string, limit, offset int32) ([]UserRecord, error)
	CountManagedUsers(ctx context.Context, candidateIDs []int64, usernameFilter, accountFilter string) (int64, error)
	ListUserRolesByUserIDs(ctx context.Context, userIDs []int64) ([]UserRoleAssignment, error)
}
