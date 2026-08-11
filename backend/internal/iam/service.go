// IAM application service (tasks T019/T020/T021; contracts/iam-application.md).
//
// The concrete Service implements the application ports against a Store:
// session lifecycle (T019), identity/authorization reads (T020) and role
// operations (T021) — migrated from internal/auth and internal/rbac with the
// same security and transaction semantics, now IAM-local and typed.
//
// Boundary rules: no pgx/sqlc/migrations imports, no Organization queries,
// no password/token/hash in event payloads or logs.
package iam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/hdw/vue-element-plus-admin/backend/internal/consistency"
)

// defaultRoleCode is assigned to every registered account so no user exists
// without at least one role (the legacy register guarantee).
const defaultRoleCode = "user"

// Service implements the IAM application ports against a Store.
type Service struct {
	store  Store
	logger *slog.Logger
}

// NewService wires the service to its data-access port. The logger is
// module-scoped by the composition root (T072); it is used for port-call
// observability, never for credentials or payload content.
func NewService(store Store, logger *slog.Logger) *Service {
	return &Service{store: store, logger: logger}
}

// Compile-time guarantee: the concrete service implements the application
// ports it is wired as. (ManagedUserService's write side lands with the
// managed-user ops in US3.)
var (
	_ AuthService        = (*Service)(nil)
	_ IdentityService    = (*Service)(nil)
	_ RoleService        = (*Service)(nil)
	_ AdminBootstrap     = (*Service)(nil)
	_ ReceiptResolver    = (*Service)(nil)
	_ ManagedUserService = (*Service)(nil)
)

// dummyHash is used to burn a comparable amount of work when a login targets
// a nonexistent user, keeping response timing uniform (anti-enumeration).
var dummyHash = func() string {
	h, err := HashPassword("dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return h
}()

// --- authentication (T019) -------------------------------------------------

// Register creates an active user, assigns the default 'user' role and issues
// a session atomically (one IAM-local transaction); the database stores only
// the token hash. Duplicate usernames return ErrUsernameTaken with no partial
// state; Organization membership is never created here.
func (s *Service) Register(ctx context.Context, username, password string, _ OperationContext) (AuthSession, error) {
	// Early uniqueness check for a clean 409 (the UNIQUE constraint is the
	// final guard inside the transaction).
	if _, err := s.store.GetUserByUsername(ctx, username); err == nil {
		return AuthSession{}, ErrUsernameTaken
	} else if !errors.Is(err, ErrUserNotFound) {
		return AuthSession{}, fmt.Errorf("check username: %w", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return AuthSession{}, fmt.Errorf("hash password: %w", err)
	}
	token, err := GenerateToken()
	if err != nil {
		return AuthSession{}, fmt.Errorf("generate token: %w", err)
	}

	now := time.Now()
	tokenHash := HashToken(token)

	var session AuthSession
	err = s.store.RunInTx(ctx, func(tx Store) error {
		user, err := tx.CreateUser(ctx, username, hash)
		if err != nil {
			return err // adapter maps 23505 -> ErrUsernameTaken
		}
		defaultRole, err := tx.GetRoleByCode(ctx, defaultRoleCode)
		if err != nil {
			return fmt.Errorf("default role %q: %w", defaultRoleCode, err)
		}
		if err := tx.InsertUserRole(ctx, user.ID, defaultRole.ID); err != nil {
			return fmt.Errorf("assign default role: %w", err)
		}
		sessionID, err := tx.CreateSession(ctx, tokenHash, user.ID, SessionExpiry(now))
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		session = AuthSession{
			Token:     token,
			TokenType: "Bearer",
			ExpiresIn: SessionTTL,
			User: Principal{
				UserID:    user.ID,
				Username:  user.Username,
				SessionID: strconv.FormatInt(sessionID, 10),
			},
		}
		return nil
	})
	if err != nil {
		return AuthSession{}, err
	}
	return session, nil
}

// Login verifies credentials and issues a session. Nonexistent username, wrong
// password, provisioning and disabled states all return ErrInvalidCredentials
// with uniform timing (a nonexistent user burns a comparable dummy hash).
func (s *Service) Login(ctx context.Context, username, password string, _ OperationContext) (AuthSession, error) {
	user, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// Burn comparable work so timing does not reveal whether the user exists.
			_, _ = VerifyPassword(dummyHash, password)
			return AuthSession{}, ErrInvalidCredentials
		}
		return AuthSession{}, fmt.Errorf("get user: %w", err)
	}

	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		return AuthSession{}, ErrInvalidCredentials
	}
	if user.LifecycleState != LifecycleActive {
		return AuthSession{}, ErrInvalidCredentials
	}

	token, err := GenerateToken()
	if err != nil {
		return AuthSession{}, fmt.Errorf("generate token: %w", err)
	}
	now := time.Now()
	sessionID, err := s.store.CreateSession(ctx, HashToken(token), user.ID, SessionExpiry(now))
	if err != nil {
		return AuthSession{}, fmt.Errorf("create session: %w", err)
	}

	return AuthSession{
		Token:     token,
		TokenType: "Bearer",
		ExpiresIn: SessionTTL,
		User: Principal{
			UserID:    user.ID,
			Username:  user.Username,
			SessionID: strconv.FormatInt(sessionID, 10),
		},
	}, nil
}

// RevokeSession revokes a session by raw token. Unknown/expired/revoked
// tokens are idempotent success; no token-validity detail is returned.
func (s *Service) RevokeSession(ctx context.Context, rawToken string, _ OperationContext) error {
	return s.store.RevokeSessionByTokenHash(ctx, HashToken(rawToken))
}

// Authenticate validates a raw token — hash match, revocation, sliding idle
// expiry, absolute lifetime cap and active user state — and returns the
// Principal (raw token is never included). Every invalid-state variant maps
// to ErrInvalidToken; valid sessions are slid forward (renewed).
func (s *Service) Authenticate(ctx context.Context, rawToken string, _ OperationContext) (Principal, error) {
	row, err := s.store.GetSessionByTokenHash(ctx, HashToken(rawToken))
	if err != nil {
		if errors.Is(err, ErrInvalidToken) {
			return Principal{}, err
		}
		return Principal{}, fmt.Errorf("get session: %w", err)
	}

	now := time.Now()
	if row.RevokedAt != nil {
		return Principal{}, ErrInvalidToken
	}
	if !SessionIsValid(now, row.CreatedAt, row.ExpiresAt) {
		return Principal{}, ErrInvalidToken
	}
	if row.LifecycleState != LifecycleActive {
		return Principal{}, ErrInvalidToken
	}

	// Sliding renewal: extend expires_at (and bump last_used_at) on each use.
	if err := s.store.TouchSession(ctx, row.TokenHash, RenewExpiry(now)); err != nil {
		return Principal{}, fmt.Errorf("touch session: %w", err)
	}

	// SessionID is the stable DB session id, matching Register/Login so every
	// Principal in a session's life carries the same reference.
	return Principal{UserID: row.UserID, Username: row.Username, SessionID: strconv.FormatInt(row.ID, 10)}, nil
}

// --- identity and authorization reads (T020) -------------------------------

// GetIdentity returns the identity aggregate; no Organization fields.
func (s *Service) GetIdentity(ctx context.Context, userID int64) (IdentityProfile, error) {
	u, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return IdentityProfile{}, err
	}
	return IdentityProfile{
		ID:             u.ID,
		Username:       u.Username,
		Account:        u.Account,
		Email:          u.Email,
		LifecycleState: u.LifecycleState,
		Version:        u.Version,
		CreatedAt:      u.CreatedAt,
		UpdatedAt:      u.UpdatedAt,
	}, nil
}

// GetAuthorizationProfile computes roles and effective permissions from IAM
// tables only. Permissions are unique and sorted (SQL DISTINCT + ORDER BY);
// empty results are non-null empty slices, never nil.
func (s *Service) GetAuthorizationProfile(ctx context.Context, userID int64) (AuthorizationProfile, error) {
	roles, err := s.store.ListRolesByUserID(ctx, userID)
	if err != nil {
		return AuthorizationProfile{}, err
	}
	permissions, err := s.store.ListEffectivePermissionsByUserID(ctx, userID)
	if err != nil {
		return AuthorizationProfile{}, err
	}
	summaries := make([]RoleSummary, 0, len(roles))
	for _, r := range roles {
		summaries = append(summaries, toRoleSummary(r))
	}
	if permissions == nil {
		permissions = []string{}
	}
	return AuthorizationProfile{Roles: summaries, EffectivePermissions: permissions}, nil
}

// HasPermission queries current grants on every invocation (no
// authorization-decision cache); unknown permission codes return false and
// never grant access.
func (s *Service) HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error) {
	return s.store.HasPermissionByUserID(ctx, userID, permissionCode)
}

// BatchGetManagedIdentities filters and paginates inside IAM. A nil
// candidateIDs means all normal managed users; an empty list yields an empty
// result without query expansion. Provisioning users are excluded; role
// projections are aggregated in one bounded query, never per-row.
func (s *Service) BatchGetManagedIdentities(ctx context.Context, candidateUserIDs []int64,
	usernameFilter, accountFilter *string, pageIndex, pageSize int) (ManagedIdentityPage, error) {
	if candidateUserIDs != nil && len(candidateUserIDs) == 0 {
		return ManagedIdentityPage{Items: []ManagedIdentity{}, Total: 0}, nil
	}

	// Legacy pagination clamps: pageSize default 10, max 100; pageIndex min 1.
	if pageSize < 1 {
		pageSize = 10
	}
	pageSize = min(pageSize, 100)
	pageIndex = max(pageIndex, 1)
	limit := int32(pageSize)
	offset := int32((pageIndex - 1) * pageSize)

	uf, af := "", ""
	if usernameFilter != nil {
		uf = *usernameFilter
	}
	if accountFilter != nil {
		af = *accountFilter
	}

	users, err := s.store.ListManagedUsers(ctx, candidateUserIDs, uf, af, limit, offset)
	if err != nil {
		return ManagedIdentityPage{}, err
	}
	total, err := s.store.CountManagedUsers(ctx, candidateUserIDs, uf, af)
	if err != nil {
		return ManagedIdentityPage{}, err
	}

	items := make([]ManagedIdentity, 0, len(users))
	if len(users) > 0 {
		ids := make([]int64, 0, len(users))
		for _, u := range users {
			ids = append(ids, u.ID)
		}
		assignments, err := s.store.ListUserRolesByUserIDs(ctx, ids)
		if err != nil {
			return ManagedIdentityPage{}, err
		}
		rolesByUser := make(map[int64][]RoleSummary, len(users))
		for _, a := range assignments {
			rolesByUser[a.UserID] = append(rolesByUser[a.UserID], toRoleSummary(a.Role))
		}
		for _, u := range users {
			roles := rolesByUser[u.ID]
			if roles == nil {
				roles = []RoleSummary{}
			}
			items = append(items, ManagedIdentity{
				IdentityProfile: IdentityProfile{
					ID:             u.ID,
					Username:       u.Username,
					Account:        u.Account,
					Email:          u.Email,
					LifecycleState: u.LifecycleState,
					Version:        u.Version,
					CreatedAt:      u.CreatedAt,
					UpdatedAt:      u.UpdatedAt,
				},
				Roles: roles,
			})
		}
	}
	return ManagedIdentityPage{Items: items, Total: total}, nil
}

// --- role operations (T021) ------------------------------------------------

// ListRoles returns the current public role fields in deterministic (id) order.
func (s *Service) ListRoles(ctx context.Context) ([]RoleSummary, error) {
	rows, err := s.store.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoleSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRoleSummary(r))
	}
	return out, nil
}

// ValidateRoleIDs validates the entire set — any missing role returns
// ErrRoleNotFound with no writes — and duplicate input IDs do not duplicate
// results.
func (s *Service) ValidateRoleIDs(ctx context.Context, ids []int64) ([]RoleSummary, error) {
	seen := make(map[int64]bool, len(ids))
	out := make([]RoleSummary, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		role, err := s.store.GetRoleByID(ctx, id)
		if err != nil {
			return nil, err
		}
		seen[id] = true
		out = append(out, toRoleSummary(role))
	}
	return out, nil
}

// SaveRole creates (nil id) or updates (with id) a role. Name/code uniqueness
// is enforced in the database (ErrNameTaken); built-in codes are immutable.
func (s *Service) SaveRole(ctx context.Context, _ OperationContext, id *int64, name, code string) (int64, error) {
	if id != nil {
		existing, err := s.store.GetRoleByID(ctx, *id)
		if err != nil {
			return 0, err
		}
		if isBuiltinRoleCode(existing.Code) && code != existing.Code {
			return 0, ErrBuiltinRoleCodeImmutable
		}
		updated, err := s.store.UpdateRole(ctx, *id, name, code)
		if err != nil {
			return 0, err
		}
		return updated.ID, nil
	}
	created, err := s.store.CreateRole(ctx, name, code)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// DeleteRoles removes roles atomically in one IAM-local transaction, refusing
// built-ins and roles still referenced by users. The full batch is validated
// before any row is deleted — one invalid target deletes nothing.
func (s *Service) DeleteRoles(ctx context.Context, _ OperationContext, ids []int64) error {
	return s.store.RunInTx(ctx, func(tx Store) error {
		for _, id := range ids {
			role, err := tx.GetRoleByID(ctx, id)
			if err != nil {
				return err
			}
			if isBuiltinRoleCode(role.Code) {
				return ErrBuiltinRoleDeleteProtected
			}
			n, err := tx.CountUserRolesByRoleID(ctx, id)
			if err != nil {
				return fmt.Errorf("count role users: %w", err)
			}
			if n > 0 {
				return fmt.Errorf("%w: role %d still has users", ErrDeleteProtected, id)
			}
		}
		for _, id := range ids {
			if err := tx.DeleteRole(ctx, id); err != nil {
				return fmt.Errorf("delete role: %w", err)
			}
		}
		return nil
	})
}

// --- admin bootstrap (T023) ---------------------------------------------------

// GrantBuiltInAdminRole grants a registered user the admin or super_admin
// built-in role. Only those two codes are accepted; the user must already be
// registered (ErrUserNotFound otherwise). The grant is idempotent: an
// already-granted user keeps the version unchanged, while a new grant bumps
// users.version inside the same transaction (latest row locked for update, so
// concurrent grants cannot race). All other role assignments are preserved.
func (s *Service) GrantBuiltInAdminRole(ctx context.Context, username, roleCode string) (GrantAdminResult, error) {
	if roleCode != "admin" && roleCode != "super_admin" {
		return GrantAdminResult{}, NewInvalidInput("role must be admin or super_admin", []FieldError{{
			Field:   "roleCode",
			Code:    "invalid_role",
			Message: "must be admin or super_admin",
		}})
	}

	var result GrantAdminResult
	err := s.store.RunInTx(ctx, func(tx Store) error {
		user, err := tx.GetUserByUsernameForUpdate(ctx, username)
		if err != nil {
			return err
		}
		role, err := tx.GetRoleByCode(ctx, roleCode)
		if err != nil {
			return fmt.Errorf("built-in role %q: %w", roleCode, err)
		}

		result.UserID = user.ID
		result.ResultingVersion = user.Version

		roles, err := tx.ListRolesByUserID(ctx, user.ID)
		if err != nil {
			return fmt.Errorf("list user roles: %w", err)
		}
		for _, r := range roles {
			if r.Code == roleCode {
				return nil // idempotent: already granted, no version bump
			}
		}

		if err := tx.InsertUserRole(ctx, user.ID, role.ID); err != nil {
			return fmt.Errorf("grant role: %w", err)
		}
		if err := tx.BumpUserVersion(ctx, user.ID); err != nil {
			return fmt.Errorf("bump user version: %w", err)
		}
		result.ResultingVersion = user.Version + 1
		return nil
	})
	if err != nil {
		return GrantAdminResult{}, err
	}
	return result, nil
}

// --- managed-user lifecycle (US3) ----------------------------------------------

// CreateProvisioningUser creates a provisioning-state identity with its
// password hash, assigns the role set and commits the command receipt in one
// IAM transaction (contracts/iam-application.md CreateProvisioningUser). The
// password is consumed inside this request only — the hash is persisted, the
// raw password never reaches the receipt or any log.
//
// Retry with the same operation ID replays the committed user ID/version
// without duplicate side effects; a differing request is OperationConflict.
// The provisioning user cannot log in and is excluded from managed lists
// (both enforced by existing Login / ListManagedUsers lifecycle checks).
func (s *Service) CreateProvisioningUser(ctx context.Context, requestCtx OperationContext,
	username string, account, email *string, password string, roleIDs []int64) (CreateUserResult, error) {
	const commandName = "create_provisioning_user"
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"username":                  username,
		"account":                   consistency.OptString(account),
		"email":                     consistency.OptString(email),
		"role_ids":                  consistency.SortedIDs(roleIDs),
		"password_change_requested": true,
	})
	if err != nil {
		return CreateUserResult{}, err
	}

	// Resolve-first: a previously committed operation replays its stored result
	// instead of re-running the side effect (crash/timeout retry path). A replay
	// whose request differs from the committed evidence is an operation
	// conflict, never a re-application.
	committed, err := s.store.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
	if err != nil {
		return CreateUserResult{}, err
	}
	if committed != nil {
		if committed.RequestFingerprint != fingerprint {
			return CreateUserResult{}, fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
		}
		return receiptCreateResult(*committed), nil
	}

	hash, err := HashPassword(password)
	if err != nil {
		return CreateUserResult{}, fmt.Errorf("hash password: %w", err)
	}

	var result CreateUserResult
	err = s.store.RunInTx(ctx, func(tx Store) error {
		user, err := tx.CreateProvisioningUser(ctx, username, hash, account, email)
		if err != nil {
			return err // adapter maps 23505 -> ErrUsernameTaken
		}
		// Role assignment is validated and inserted inside the same transaction:
		// a missing role rejects the whole provisioning, leaving nothing behind.
		for _, roleID := range roleIDs {
			if _, err := tx.GetRoleByID(ctx, roleID); err != nil {
				return err
			}
			if err := tx.InsertUserRole(ctx, user.ID, roleID); err != nil {
				return fmt.Errorf("assign role %d: %w", roleID, err)
			}
		}
		result = CreateUserResult{UserID: user.ID, ResultingVersion: user.Version}
		return tx.SaveCommandReceipt(ctx, IAMCommandReceipt{
			OperationID:        requestCtx.OperationID,
			CommandName:        commandName,
			RequestFingerprint: fingerprint,
			Status:             CommandSucceeded,
			SubjectID:          &user.ID,
			ResultingVersion:   &result.ResultingVersion,
			SafeResult: map[string]any{
				"user_id":           float64(user.ID),
				"resulting_version": float64(result.ResultingVersion),
			},
		})
	})
	if err != nil {
		return CreateUserResult{}, err
	}
	return result, nil
}

// ActivateUser transitions a provisioning user to active, only when
// users.version == expectedVersion (contracts/iam-application.md ActivateUser).
// The single-statement CAS is the atomic decision: a miss is classified from
// the current row inside the same transaction — missing user (ErrUserNotFound),
// non-provisioning state (ErrInvalidLifecycleTransition) or stale version
// (ErrVersionConflict). The transition and its receipt commit atomically;
// replaying the same operation returns the committed resulting version, never
// a second transition.
func (s *Service) ActivateUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64) (int64, error) {
	const commandName = "activate_user"
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"user_id":          userID,
		"expected_version": expectedVersion,
	})
	if err != nil {
		return 0, err
	}

	committed, err := s.store.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
	if err != nil {
		return 0, err
	}
	if committed != nil {
		if committed.RequestFingerprint != fingerprint {
			return 0, fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
		}
		if committed.ResultingVersion == nil {
			return 0, fmt.Errorf("%w: committed receipt carries no resulting version", ErrInternal)
		}
		return *committed.ResultingVersion, nil
	}

	var resultingVersion int64
	err = s.store.RunInTx(ctx, func(tx Store) error {
		version, hit, err := tx.ActivateUserCAS(ctx, userID, expectedVersion)
		if err != nil {
			return err
		}
		if !hit {
			// CAS missed: the row is untouched, so the current row tells which
			// precondition failed. Missing user wins, then state, then version.
			user, err := tx.GetUserByID(ctx, userID)
			if err != nil {
				return err // adapter maps absence to ErrUserNotFound
			}
			if user.LifecycleState != LifecycleProvisioning {
				return fmt.Errorf("%w: user %d is %s, expected provisioning",
					ErrInvalidLifecycleTransition, userID, user.LifecycleState)
			}
			return fmt.Errorf("%w: user %d is at version %d, expected %d",
				ErrVersionConflict, userID, user.Version, expectedVersion)
		}
		resultingVersion = version
		return tx.SaveCommandReceipt(ctx, IAMCommandReceipt{
			OperationID:        requestCtx.OperationID,
			CommandName:        commandName,
			RequestFingerprint: fingerprint,
			Status:             CommandSucceeded,
			SubjectID:          &userID,
			ResultingVersion:   &resultingVersion,
			SafeResult: map[string]any{
				"user_id":           float64(userID),
				"resulting_version": float64(resultingVersion),
			},
		})
	})
	if err != nil {
		return 0, err
	}
	return resultingVersion, nil
}

// UpdateManagedUser updates profile, optional credential and the role set of a
// managed user, only when users.version == expectedVersion
// (contracts/iam-application.md UpdateManagedUser). The transaction validates
// every role before any write, then applies the version-gated profile update
// (CAS), replaces the role set and — only when a non-blank password was
// supplied — swaps the hash (the row is already locked by the CAS). A blank or
// absent password preserves the existing hash; a stale version rejects the
// whole update, so there is never a partial profile/credential/role change.
// The command receipt commits in the same transaction; replaying the same
// operation returns the committed resulting version. There is no Organization
// membership parameter.
func (s *Service) UpdateManagedUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64,
	username string, account, email *string, password *string, roleIDs []int64) (int64, error) {
	const commandName = "update_managed_user"
	passwordChangeRequested := password != nil && *password != ""
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"user_id":                   userID,
		"expected_version":          expectedVersion,
		"username":                  username,
		"account":                   consistency.OptString(account),
		"email":                     consistency.OptString(email),
		"role_ids":                  consistency.SortedIDs(roleIDs),
		"password_change_requested": passwordChangeRequested,
	})
	if err != nil {
		return 0, err
	}

	committed, err := s.store.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
	if err != nil {
		return 0, err
	}
	if committed != nil {
		if committed.RequestFingerprint != fingerprint {
			return 0, fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
		}
		if committed.ResultingVersion == nil {
			return 0, fmt.Errorf("%w: committed receipt carries no resulting version", ErrInternal)
		}
		return *committed.ResultingVersion, nil
	}

	// Hash only when the request actually carries a new non-blank password;
	// blank/absent preserves the existing hash by omitting the credential step.
	var passwordHash *string
	if passwordChangeRequested {
		hash, err := HashPassword(*password)
		if err != nil {
			return 0, fmt.Errorf("hash password: %w", err)
		}
		passwordHash = &hash
	}

	var resultingVersion int64
	err = s.store.RunInTx(ctx, func(tx Store) error {
		// Full role validation before any write: one missing role rejects the
		// whole update. SortedIDs dedupes the insertion set as well.
		sortedRoleIDs := consistency.SortedIDs(roleIDs)
		for _, roleID := range sortedRoleIDs {
			if _, err := tx.GetRoleByID(ctx, roleID); err != nil {
				return err
			}
		}

		version, hit, err := tx.UpdateManagedUserCAS(ctx, userID, expectedVersion, username, account, email)
		if err != nil {
			return err // adapter maps 23505 -> ErrUsernameTaken
		}
		if !hit {
			// CAS missed: the row is untouched; a missing user is the only
			// other cause, so the current row classifies the failure.
			user, err := tx.GetUserByID(ctx, userID)
			if err != nil {
				return err // adapter maps absence to ErrUserNotFound
			}
			return fmt.Errorf("%w: user %d is at version %d, expected %d",
				ErrVersionConflict, userID, user.Version, expectedVersion)
		}
		resultingVersion = version

		// Role replacement: clear, then insert the validated set.
		if err := tx.DeleteUserRolesByUserID(ctx, userID); err != nil {
			return fmt.Errorf("replace roles: %w", err)
		}
		for _, roleID := range sortedRoleIDs {
			if err := tx.InsertUserRole(ctx, userID, roleID); err != nil {
				return fmt.Errorf("assign role %d: %w", roleID, err)
			}
		}
		// The credential swap runs only when a new hash exists; the CAS above
		// holds the row lock, so it cannot interleave with a concurrent update.
		if passwordHash != nil {
			if err := tx.UpdateUserPasswordHash(ctx, userID, *passwordHash); err != nil {
				return fmt.Errorf("update password hash: %w", err)
			}
		}
		return tx.SaveCommandReceipt(ctx, IAMCommandReceipt{
			OperationID:        requestCtx.OperationID,
			CommandName:        commandName,
			RequestFingerprint: fingerprint,
			Status:             CommandSucceeded,
			SubjectID:          &userID,
			ResultingVersion:   &resultingVersion,
			SafeResult: map[string]any{
				"user_id":           float64(userID),
				"resulting_version": float64(resultingVersion),
			},
		})
	})
	if err != nil {
		return 0, err
	}
	return resultingVersion, nil
}

// userDeletedEventNamespace seeds deterministic version-5 UUIDs for outbox
// event IDs: deriving from the operation id + subject means a retried delete
// can never generate a second event id for the same logical deletion.
var userDeletedEventNamespace = uuid.MustParse("8f3c4b2a-0000-4000-8000-000000000001")

// DeleteUsers authoritatively deletes a batch of users in one IAM transaction
// (contracts/iam-application.md DeleteUsers): normalized targets are
// full-batch prechecked under row locks (one missing or stale target rejects
// the whole batch), then each user is deleted — sessions and user_roles
// cascade away — with one iam.user.deleted v1 outbox event carrying the
// tombstone version (prior version + 1), and a single batch receipt records
// the same normalized per-user results. Deletion, events and receipt commit
// together; a retry with the same operation ID replays the committed results
// and never duplicates outbox events.
func (s *Service) DeleteUsers(ctx context.Context, requestCtx OperationContext, targets []DeleteTarget) ([]BatchDeleteResultItem, error) {
	const commandName = "delete_users"

	normalized := normalizeDeleteTargets(targets)
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"targets": deleteTargetArgs(normalized),
	})
	if err != nil {
		return nil, err
	}

	committed, err := s.store.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
	if err != nil {
		return nil, err
	}
	if committed != nil {
		if committed.RequestFingerprint != fingerprint {
			return nil, fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
		}
		return deleteResultFromReceipt(*committed)
	}

	var results []BatchDeleteResultItem
	err = s.store.RunInTx(ctx, func(tx Store) error {
		// Full-batch precheck under row locks: any missing or stale target
		// rejects the entire batch before a single row is deleted.
		now := time.Now()
		tombstones := make([]BatchDeleteResultItem, 0, len(normalized))
		for _, target := range normalized {
			user, err := tx.GetUserByIDForUpdate(ctx, target.UserID)
			if err != nil {
				return err // adapter maps absence to ErrUserNotFound
			}
			if user.Version != target.ExpectedVersion {
				return fmt.Errorf("%w: user %d is at version %d, expected %d",
					ErrVersionConflict, target.UserID, user.Version, target.ExpectedVersion)
			}
			tombstones = append(tombstones, BatchDeleteResultItem{
				UserID:           target.UserID,
				TombstoneVersion: user.Version + 1,
			})
		}

		// Delete (sessions/user_roles cascade) then emit one event per user;
		// the tombstone version is the event aggregate version.
		for _, item := range tombstones {
			if err := tx.DeleteUser(ctx, item.UserID); err != nil {
				return fmt.Errorf("delete user %d: %w", item.UserID, err)
			}
			if err := tx.EnqueueOutboxEvent(ctx, OutboxEventRecord{
				EventID:          uuid.NewSHA1(userDeletedEventNamespace, []byte(requestCtx.OperationID+":"+strconv.FormatInt(item.UserID, 10))).String(),
				EventType:        "iam.user.deleted",
				EventVersion:     1,
				Producer:         "iam",
				AggregateType:    "user",
				AggregateID:      strconv.FormatInt(item.UserID, 10),
				AggregateVersion: item.TombstoneVersion,
				Payload:          map[string]any{"user_id": float64(item.UserID)},
				CorrelationID:    requestCtx.CorrelationID,
				OccurredAt:       now,
			}); err != nil {
				return fmt.Errorf("enqueue user.deleted for %d: %w", item.UserID, err)
			}
		}

		results = tombstones
		return tx.SaveCommandReceipt(ctx, IAMCommandReceipt{
			OperationID:        requestCtx.OperationID,
			CommandName:        commandName,
			RequestFingerprint: fingerprint,
			Status:             CommandSucceeded,
			SafeResult:         deleteTargetSafeResult(tombstones),
		})
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// normalizeDeleteTargets deduplicates identical (user_id, expected_version)
// targets and sorts ascending by user_id then version; the fingerprint and the
// executed batch always use the same normalized set.
func normalizeDeleteTargets(targets []DeleteTarget) []DeleteTarget {
	seen := make(map[DeleteTarget]bool, len(targets))
	out := make([]DeleteTarget, 0, len(targets))
	for _, t := range targets {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].ExpectedVersion < out[j].ExpectedVersion
	})
	return out
}

// deleteTargetArgs renders the canonical fingerprint args form: an array of
// {user_id, expected_version} objects, already normalized.
func deleteTargetArgs(targets []DeleteTarget) []any {
	args := make([]any, 0, len(targets))
	for _, t := range targets {
		args = append(args, map[string]any{
			"user_id":          t.UserID,
			"expected_version": t.ExpectedVersion,
		})
	}
	return args
}

// deleteTargetSafeResult is the transport-neutral receipt result: the same
// normalized per-user tombstone versions returned synchronously.
func deleteTargetSafeResult(results []BatchDeleteResultItem) map[string]any {
	targets := make([]any, 0, len(results))
	for _, r := range results {
		targets = append(targets, map[string]any{
			"user_id":           float64(r.UserID),
			"tombstone_version": float64(r.TombstoneVersion),
		})
	}
	return map[string]any{"targets": targets}
}

// deleteResultFromReceipt replays a committed delete receipt into the result
// shape; the receipt carries the same normalized targets as the synchronous
// response.
func deleteResultFromReceipt(r IAMCommandReceipt) ([]BatchDeleteResultItem, error) {
	targets, ok := r.SafeResult["targets"].([]any)
	if !ok {
		return nil, fmt.Errorf("%w: committed delete receipt carries no targets", ErrInternal)
	}
	out := make([]BatchDeleteResultItem, 0, len(targets))
	for _, t := range targets {
		m, ok := t.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: malformed committed delete target", ErrInternal)
		}
		userID, ok1 := m["user_id"].(float64)
		tombstone, ok2 := m["tombstone_version"].(float64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%w: malformed committed delete target", ErrInternal)
		}
		out = append(out, BatchDeleteResultItem{UserID: int64(userID), TombstoneVersion: int64(tombstone)})
	}
	return out, nil
}

// DisableUser transitions an active user to disabled with an expected-version
// CAS (contracts/iam-application.md DisableUser). The lifecycle change, the
// revocation of every live session and the command receipt commit in one IAM
// transaction; re-enable is out of scope, so only active -> disabled is
// legal. reason_code is a safe classification (lowercase letters, digits,
// underscore, dot — never free-form text that could carry secrets or PII).
// Replaying the same operation returns the committed resulting version.
func (s *Service) DisableUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64, reasonCode string) (int64, error) {
	const commandName = "disable_user"
	if !validSafeReasonCode(reasonCode) {
		return 0, NewInvalidInput("reason_code must be a safe classification", []FieldError{{
			Field:   "reasonCode",
			Code:    "invalid_reason_code",
			Message: "lowercase letters, digits, underscore or dot only",
		}})
	}
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"user_id":          userID,
		"expected_version": expectedVersion,
		"reason_code":      reasonCode,
	})
	if err != nil {
		return 0, err
	}

	committed, err := s.store.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
	if err != nil {
		return 0, err
	}
	if committed != nil {
		if committed.RequestFingerprint != fingerprint {
			return 0, fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
		}
		if committed.ResultingVersion == nil {
			return 0, fmt.Errorf("%w: committed receipt carries no resulting version", ErrInternal)
		}
		return *committed.ResultingVersion, nil
	}

	var resultingVersion int64
	err = s.store.RunInTx(ctx, func(tx Store) error {
		version, hit, err := tx.DisableUserCAS(ctx, userID, expectedVersion)
		if err != nil {
			return err
		}
		if !hit {
			// CAS missed: the row is untouched, so the current row tells which
			// precondition failed — missing user wins, then state, then version.
			user, err := tx.GetUserByID(ctx, userID)
			if err != nil {
				return err // adapter maps absence to ErrUserNotFound
			}
			if user.LifecycleState != LifecycleActive {
				return fmt.Errorf("%w: user %d is %s, expected active",
					ErrInvalidLifecycleTransition, userID, user.LifecycleState)
			}
			return fmt.Errorf("%w: user %d is at version %d, expected %d",
				ErrVersionConflict, userID, user.Version, expectedVersion)
		}
		resultingVersion = version
		// Revoke every live session in the same transaction: the disabled
		// subject can no longer use any existing session.
		if err := tx.RevokeSessionsByUserID(ctx, userID); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		return tx.SaveCommandReceipt(ctx, IAMCommandReceipt{
			OperationID:        requestCtx.OperationID,
			CommandName:        commandName,
			RequestFingerprint: fingerprint,
			Status:             CommandSucceeded,
			SubjectID:          &userID,
			ResultingVersion:   &resultingVersion,
			SafeResult: map[string]any{
				"user_id":           float64(userID),
				"resulting_version": float64(resultingVersion),
			},
		})
	})
	if err != nil {
		return 0, err
	}
	return resultingVersion, nil
}

// CompensateProvisioningUser deletes a workflow-created subject exactly while
// it remains provisioning at the expected version
// (contracts/iam-application.md CompensateProvisioningUser). The command has
// one outcome — physical delete, never disable — so a failed create can never
// become a visible disabled user or permanently occupy its username. The CAS
// delete, the normal iam.user.deleted event (tombstone = prior version + 1)
// and the receipt commit atomically; active/disabled/newer-version subjects
// return a typed conflict requiring reconciliation. Same-operation replay
// resolves through its receipt.
func (s *Service) CompensateProvisioningUser(ctx context.Context, requestCtx OperationContext, userID, expectedVersion int64) error {
	const commandName = "compensate_provisioning_user"
	fingerprint, err := consistency.FingerprintV1(commandName, requestCtx.ActorUserID, map[string]any{
		"user_id":          userID,
		"expected_version": expectedVersion,
	})
	if err != nil {
		return err
	}

	committed, err := s.store.GetCommandReceipt(ctx, requestCtx.OperationID, commandName)
	if err != nil {
		return err
	}
	if committed != nil {
		if committed.RequestFingerprint != fingerprint {
			return fmt.Errorf("%w: replay fingerprint does not match committed receipt", ErrOperationConflict)
		}
		return nil
	}

	err = s.store.RunInTx(ctx, func(tx Store) error {
		deletedVersion, hit, err := tx.DeleteProvisioningUserCAS(ctx, userID, expectedVersion)
		if err != nil {
			return err
		}
		if !hit {
			// CAS missed: active/disabled subjects and newer versions escalate
			// to reconciliation; a missing subject surfaces as UserNotFound.
			user, err := tx.GetUserByID(ctx, userID)
			if err != nil {
				return err // adapter maps absence to ErrUserNotFound
			}
			if user.LifecycleState != LifecycleProvisioning {
				return fmt.Errorf("%w: user %d is %s and needs reconciliation",
					ErrInvalidLifecycleTransition, userID, user.LifecycleState)
			}
			return fmt.Errorf("%w: user %d is at version %d, expected %d",
				ErrVersionConflict, userID, user.Version, expectedVersion)
		}
		// The normal deletion event keeps any partial Organization membership
		// cleanable; the tombstone version is the deleted prior version + 1.
		if err := tx.EnqueueOutboxEvent(ctx, OutboxEventRecord{
			EventID:          uuid.NewSHA1(userDeletedEventNamespace, []byte(requestCtx.OperationID+":"+strconv.FormatInt(userID, 10))).String(),
			EventType:        "iam.user.deleted",
			EventVersion:     1,
			Producer:         "iam",
			AggregateType:    "user",
			AggregateID:      strconv.FormatInt(userID, 10),
			AggregateVersion: deletedVersion + 1,
			Payload:          map[string]any{"user_id": float64(userID)},
			CorrelationID:    requestCtx.CorrelationID,
			OccurredAt:       time.Now(),
		}); err != nil {
			return fmt.Errorf("enqueue user.deleted: %w", err)
		}
		return tx.SaveCommandReceipt(ctx, IAMCommandReceipt{
			OperationID:        requestCtx.OperationID,
			CommandName:        commandName,
			RequestFingerprint: fingerprint,
			Status:             CommandSucceeded,
			SubjectID:          &userID,
			SafeResult: map[string]any{
				"user_id": float64(userID),
			},
		})
	})
	if err != nil {
		return err
	}
	return nil
}

// validSafeReasonCode restricts reason codes to safe classification tokens:
// lowercase letters, digits, underscore and dot — never free-form text that
// could carry secrets or PII.
func validSafeReasonCode(code string) bool {
	if code == "" {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		lower := c >= 'a' && c <= 'z'
		digit := c >= '0' && c <= '9'
		allowedPunct := c == '_' || (c == '.' && i > 0)
		if !lower && !digit && !allowedPunct {
			return false
		}
	}
	return true
}

// receiptCreateResult replays a committed create receipt into the result
// shape; the safe result carries only IDs/versions, never credentials.
func receiptCreateResult(r IAMCommandReceipt) CreateUserResult {
	res := CreateUserResult{}
	if r.SubjectID != nil {
		res.UserID = *r.SubjectID
	}
	if r.ResultingVersion != nil {
		res.ResultingVersion = *r.ResultingVersion
	}
	return res
}

// --- command resolution (T044) -------------------------------------------------

// ResolveCommand resolves a previously committed command after timeout or
// crash: absent returns (nil, nil) — resolution never fabricates a failure; a
// fingerprint mismatch is OperationConflict.
func (s *Service) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*IAMCommandReceipt, error) {
	receipt, err := s.store.GetCommandReceipt(ctx, operationID, commandName)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, nil
	}
	if receipt.RequestFingerprint != expectedFingerprint {
		return nil, fmt.Errorf("%w: fingerprint %q does not match committed %q",
			ErrOperationConflict, expectedFingerprint, receipt.RequestFingerprint)
	}
	return receipt, nil
}

// --- helpers ---------------------------------------------------------------

// toRoleSummary maps a store role row to the public projection; built-in
// classification is service policy derived from the code.
func toRoleSummary(r RoleRecord) RoleSummary {
	return RoleSummary{
		ID:        r.ID,
		Name:      r.Name,
		Code:      r.Code,
		IsBuiltin: isBuiltinRoleCode(r.Code),
		CreatedAt: r.CreatedAt,
	}
}

// isBuiltinRoleCode classifies the seeded catalog codes (legacy rbac
// semantics; the classification is never stored).
func isBuiltinRoleCode(code string) bool {
	switch code {
	case "super_admin", "admin", "user":
		return true
	default:
		return false
	}
}
