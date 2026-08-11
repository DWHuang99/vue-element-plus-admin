// IAM PostgreSQL adapter (task T022).
//
// Implements iam.Store against PostgreSQL using ONLY the IAM-owned generated
// sqlc package (boundary rule): no global/internal/database/sqlc, no
// Organization or BFF queries, and every transaction touches only IAM tables.
//
// Absence is signalled with the IAM domain errors (ErrUserNotFound,
// ErrRoleNotFound, ErrInvalidToken for session lookups); unique violations
// surface as ErrUsernameTaken / ErrNameTaken — the service never parses
// pgconn errors itself.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres/sqlc"
)

// queries carries the query set shared by the pool-bound Store and the
// transaction-bound txStore.
type queries struct {
	q sqlc.Querier
}

// --- users -----------------------------------------------------------------

func (s *queries) GetUserByUsername(ctx context.Context, username string) (iam.UserRecord, error) {
	row, err := s.q.GetUserByUsernameAuth(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.UserRecord{}, iam.ErrUserNotFound
		}
		return iam.UserRecord{}, err
	}
	return iam.UserRecord{
		ID:             row.ID,
		Username:       row.Username,
		PasswordHash:   row.PasswordHash,
		LifecycleState: iam.LifecycleState(row.LifecycleState),
	}, nil
}

// CreateProvisioningUser maps the nullable profile pointers to pgtype.Text for
// the generated INSERT (US3 provisioning lifecycle).
func (s *queries) CreateProvisioningUser(ctx context.Context, username, passwordHash string,
	account, email *string) (iam.UserRecord, error) {
	row, err := s.q.CreateProvisioningUser(ctx, sqlc.CreateProvisioningUserParams{
		Username:     username,
		PasswordHash: passwordHash,
		Account:      textFromPtr(account),
		Email:        textFromPtr(email),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return iam.UserRecord{}, iam.ErrUsernameTaken
		}
		return iam.UserRecord{}, err
	}
	return fullUserRecord(row.ID, row.Username, row.PasswordHash, row.Account, row.Email,
		row.LifecycleState, row.Version, row.CreatedAt, row.UpdatedAt), nil
}

// UpdateManagedUserCAS maps the profile CAS to (version, hit); a missed CAS is
// a normal (0, false, nil) result and a unique username violation surfaces as
// ErrUsernameTaken — the service classifies the miss from the current row.
func (s *queries) UpdateManagedUserCAS(ctx context.Context, userID, expectedVersion int64,
	username string, account, email *string) (int64, bool, error) {
	version, err := s.q.UpdateManagedUserCAS(ctx, sqlc.UpdateManagedUserCASParams{
		ID:       userID,
		Username: username,
		Account:  textFromPtr(account),
		Email:    textFromPtr(email),
		Version:  expectedVersion,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return 0, false, iam.ErrUsernameTaken
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return version, true, nil
}

// UpdateUserPasswordHash replaces the stored hash; only called inside the
// transaction whose UpdateManagedUserCAS already locked the row.
func (s *queries) UpdateUserPasswordHash(ctx context.Context, userID int64, passwordHash string) error {
	return s.q.UpdateUserPasswordHash(ctx, sqlc.UpdateUserPasswordHashParams{
		ID:           userID,
		PasswordHash: passwordHash,
	})
}

// DisableUserCAS maps the single-statement lifecycle CAS to (version, hit); a
// missed CAS is a normal (0, false, nil) result — the service classifies the
// failure reason from the current row.
func (s *queries) DisableUserCAS(ctx context.Context, userID, expectedVersion int64) (int64, bool, error) {
	version, err := s.q.DisableUserCAS(ctx, sqlc.DisableUserCASParams{
		ID:      userID,
		Version: expectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return version, true, nil
}

// DeleteProvisioningUserCAS maps the single-statement compensation CAS to
// (deletedVersion, hit); a missed CAS is a normal (0, false, nil) result.
func (s *queries) DeleteProvisioningUserCAS(ctx context.Context, userID, expectedVersion int64) (int64, bool, error) {
	version, err := s.q.DeleteProvisioningUserCAS(ctx, sqlc.DeleteProvisioningUserCASParams{
		ID:      userID,
		Version: expectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return version, true, nil
}

// ActivateUserCAS maps the single-statement CAS to (version, hit); a missed
// CAS is a normal (0, false, nil) result — the service distinguishes the
// failure reason, so no pgx detail leaks through the port.
func (s *queries) ActivateUserCAS(ctx context.Context, userID, expectedVersion int64) (int64, bool, error) {
	version, err := s.q.ActivateUserCAS(ctx, sqlc.ActivateUserCASParams{
		ID:      userID,
		Version: expectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return version, true, nil
}

// GetUserByUsernameForUpdate locks the user row for the granting transaction.
// The query deliberately omits password_hash (least privilege: bootstrap never
// touches credentials).
func (s *queries) GetUserByUsernameForUpdate(ctx context.Context, username string) (iam.UserRecord, error) {
	row, err := s.q.GetUserByUsernameForUpdate(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.UserRecord{}, iam.ErrUserNotFound
		}
		return iam.UserRecord{}, err
	}
	return iam.UserRecord{
		ID:             row.ID,
		Username:       row.Username,
		LifecycleState: iam.LifecycleState(row.LifecycleState),
		Version:        row.Version,
	}, nil
}

func (s *queries) BumpUserVersion(ctx context.Context, userID int64) error {
	return s.q.BumpUserVersion(ctx, userID)
}

// GetUserByIDForUpdate locks the row for the DeleteUsers batch precheck; the
// query deliberately omits password_hash (least privilege: deletion never
// touches credentials).
func (s *queries) GetUserByIDForUpdate(ctx context.Context, id int64) (iam.UserRecord, error) {
	row, err := s.q.GetUserByIDForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.UserRecord{}, iam.ErrUserNotFound
		}
		return iam.UserRecord{}, err
	}
	return iam.UserRecord{
		ID:             row.ID,
		Username:       row.Username,
		LifecycleState: iam.LifecycleState(row.LifecycleState),
		Version:        row.Version,
	}, nil
}

// DeleteUser removes the user row; sessions and user_roles cascade away via
// schema-level ON DELETE CASCADE.
func (s *queries) DeleteUser(ctx context.Context, id int64) error {
	return s.q.DeleteUser(ctx, id)
}

func (s *queries) GetUserByID(ctx context.Context, id int64) (iam.UserRecord, error) {
	row, err := s.q.GetUserByIDFull(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.UserRecord{}, iam.ErrUserNotFound
		}
		return iam.UserRecord{}, err
	}
	return fullUserRecord(row.ID, row.Username, row.PasswordHash, row.Account, row.Email,
		row.LifecycleState, row.Version, row.CreatedAt, row.UpdatedAt), nil
}

// CreateUser creates an active user at version 1 (the SQL hardcodes the
// lifecycle; the unique username constraint is the final guard).
func (s *queries) CreateUser(ctx context.Context, username, passwordHash string) (iam.UserRecord, error) {
	row, err := s.q.CreateUser(ctx, sqlc.CreateUserParams{Username: username, PasswordHash: passwordHash})
	if err != nil {
		if isUniqueViolation(err) {
			return iam.UserRecord{}, iam.ErrUsernameTaken
		}
		return iam.UserRecord{}, err
	}
	return iam.UserRecord{
		ID:             row.ID,
		Username:       row.Username,
		PasswordHash:   row.PasswordHash,
		LifecycleState: iam.LifecycleActive,
		Version:        1,
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}

// --- sessions ---------------------------------------------------------------

func (s *queries) CreateSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) (int64, error) {
	row, err := s.q.CreateSession(ctx, sqlc.CreateSessionParams{
		TokenHash: tokenHash,
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

// GetSessionByTokenHash joins the session with its owner's username and
// lifecycle state for the Authenticate checks. Absent hash -> ErrInvalidToken
// (the only consumer treats absence as an invalid token).
func (s *queries) GetSessionByTokenHash(ctx context.Context, tokenHash string) (iam.SessionRecord, error) {
	row, err := s.q.GetSessionWithUserState(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.SessionRecord{}, iam.ErrInvalidToken
		}
		return iam.SessionRecord{}, err
	}
	return iam.SessionRecord{
		ID:             row.SessionID,
		TokenHash:      row.TokenHash,
		UserID:         row.UserID,
		CreatedAt:      row.CreatedAt.Time,
		ExpiresAt:      row.ExpiresAt.Time,
		RevokedAt:      nullableTime(row.RevokedAt),
		Username:       row.UserUsername,
		LifecycleState: iam.LifecycleState(row.UserLifecycleState),
	}, nil
}

// RevokeSessionByTokenHash is an UPDATE: unknown tokens affect zero rows and
// succeed, keeping revoke idempotent per contract.
func (s *queries) RevokeSessionByTokenHash(ctx context.Context, tokenHash string) error {
	return s.q.RevokeSessionByTokenHash(ctx, tokenHash)
}

// RevokeSessionsByUserID revokes every live session of the subject
// (DisableUser runs it inside the lifecycle transaction).
func (s *queries) RevokeSessionsByUserID(ctx context.Context, userID int64) error {
	return s.q.RevokeSessionsByUserID(ctx, userID)
}

func (s *queries) TouchSession(ctx context.Context, tokenHash string, expiresAt time.Time) error {
	return s.q.TouchSession(ctx, sqlc.TouchSessionParams{
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
}

// --- roles -----------------------------------------------------------------

func (s *queries) GetRoleByCode(ctx context.Context, code string) (iam.RoleRecord, error) {
	row, err := s.q.GetRoleByCode(ctx, code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.RoleRecord{}, iam.ErrRoleNotFound
		}
		return iam.RoleRecord{}, err
	}
	return roleRecord(row.ID, row.Name, row.Code, row.CreatedAt), nil
}

func (s *queries) GetRoleByID(ctx context.Context, id int64) (iam.RoleRecord, error) {
	row, err := s.q.GetRoleByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.RoleRecord{}, iam.ErrRoleNotFound
		}
		return iam.RoleRecord{}, err
	}
	return roleRecord(row.ID, row.Name, row.Code, row.CreatedAt), nil
}

func (s *queries) ListRoles(ctx context.Context) ([]iam.RoleRecord, error) {
	rows, err := s.q.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]iam.RoleRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, roleRecord(r.ID, r.Name, r.Code, r.CreatedAt))
	}
	return out, nil
}

func (s *queries) CreateRole(ctx context.Context, name, code string) (iam.RoleRecord, error) {
	row, err := s.q.CreateRole(ctx, sqlc.CreateRoleParams{Name: name, Code: code})
	if err != nil {
		if isUniqueViolation(err) {
			return iam.RoleRecord{}, iam.ErrNameTaken
		}
		return iam.RoleRecord{}, err
	}
	return roleRecord(row.ID, row.Name, row.Code, row.CreatedAt), nil
}

func (s *queries) UpdateRole(ctx context.Context, id int64, name, code string) (iam.RoleRecord, error) {
	row, err := s.q.UpdateRole(ctx, sqlc.UpdateRoleParams{ID: id, Name: name, Code: code})
	if err != nil {
		if isUniqueViolation(err) {
			return iam.RoleRecord{}, iam.ErrNameTaken
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return iam.RoleRecord{}, iam.ErrRoleNotFound
		}
		return iam.RoleRecord{}, err
	}
	return roleRecord(row.ID, row.Name, row.Code, row.CreatedAt), nil
}

func (s *queries) DeleteRole(ctx context.Context, id int64) error {
	return s.q.DeleteRole(ctx, id)
}

func (s *queries) CountUserRolesByRoleID(ctx context.Context, roleID int64) (int64, error) {
	return s.q.CountUserRolesByRoleID(ctx, roleID)
}

// --- user<->role links ------------------------------------------------------

func (s *queries) InsertUserRole(ctx context.Context, userID, roleID int64) error {
	return s.q.InsertUserRole(ctx, sqlc.InsertUserRoleParams{UserID: userID, RoleID: roleID})
}

// DeleteUserRolesByUserID clears the role set before the replacement inserts
// (UpdateManagedUser role replacement runs inside the same transaction).
func (s *queries) DeleteUserRolesByUserID(ctx context.Context, userID int64) error {
	return s.q.DeleteUserRolesByUserID(ctx, userID)
}

func (s *queries) ListRolesByUserID(ctx context.Context, userID int64) ([]iam.RoleRecord, error) {
	rows, err := s.q.ListRolesByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	// The join query carries no created_at; it is unused in the profile projection.
	out := make([]iam.RoleRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, iam.RoleRecord{ID: r.ID, Name: r.Name, Code: r.Code})
	}
	return out, nil
}

// --- authorization -----------------------------------------------------------

func (s *queries) ListEffectivePermissionsByUserID(ctx context.Context, userID int64) ([]string, error) {
	return s.q.ListEffectivePermissionsByUserID(ctx, userID)
}

func (s *queries) HasPermissionByUserID(ctx context.Context, userID int64, permissionCode string) (bool, error) {
	return s.q.HasPermissionByUserID(ctx, sqlc.HasPermissionByUserIDParams{
		UserID: userID,
		Code:   permissionCode,
	})
}

// --- managed-user listing -----------------------------------------------------

func (s *queries) ListManagedUsers(ctx context.Context, candidateIDs []int64,
	usernameFilter, accountFilter string, limit, offset int32) ([]iam.UserRecord, error) {
	rows, err := s.q.ListManagedUsers(ctx, sqlc.ListManagedUsersParams{
		Column1: candidateIDs,
		Column2: usernameFilter,
		Column3: accountFilter,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]iam.UserRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, fullUserRecord(r.ID, r.Username, r.PasswordHash, r.Account, r.Email,
			r.LifecycleState, r.Version, r.CreatedAt, r.UpdatedAt))
	}
	return out, nil
}

func (s *queries) CountManagedUsers(ctx context.Context, candidateIDs []int64,
	usernameFilter, accountFilter string) (int64, error) {
	return s.q.CountManagedUsers(ctx, sqlc.CountManagedUsersParams{
		Column1: candidateIDs,
		Column2: usernameFilter,
		Column3: accountFilter,
	})
}

func (s *queries) ListUserRolesByUserIDs(ctx context.Context, userIDs []int64) ([]iam.UserRoleAssignment, error) {
	rows, err := s.q.ListUserRolesByUserIDs(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make([]iam.UserRoleAssignment, 0, len(rows))
	for _, r := range rows {
		out = append(out, iam.UserRoleAssignment{
			UserID: r.UserID,
			Role:   iam.RoleRecord{ID: r.RoleID, Name: r.RoleName, Code: r.RoleCode},
		})
	}
	return out, nil
}

// --- command receipts (T044) --------------------------------------------------

func (s *queries) SaveCommandReceipt(ctx context.Context, receipt iam.IAMCommandReceipt) error {
	operationID, err := uuidFromString(receipt.OperationID)
	if err != nil {
		return err
	}
	result, err := json.Marshal(receipt.SafeResult)
	if err != nil {
		return fmt.Errorf("marshal receipt result: %w", err)
	}
	return s.q.SaveCommandReceipt(ctx, sqlc.SaveCommandReceiptParams{
		OperationID:        operationID,
		CommandName:        receipt.CommandName,
		RequestFingerprint: receipt.RequestFingerprint,
		Status:             string(receipt.Status),
		SubjectID:          int8FromPtr(receipt.SubjectID),
		Result:             result,
		ErrorCode:          textFromPtr(receipt.ErrorCode),
	})
}

func (s *queries) GetCommandReceipt(ctx context.Context, operationID, commandName string) (*iam.IAMCommandReceipt, error) {
	parsed, err := uuidFromString(operationID)
	if err != nil {
		return nil, err
	}
	row, err := s.q.GetCommandReceipt(ctx, sqlc.GetCommandReceiptParams{
		OperationID: parsed, CommandName: commandName,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	safeResult := map[string]any{}
	if len(row.Result) > 0 {
		if err := json.Unmarshal(row.Result, &safeResult); err != nil {
			return nil, fmt.Errorf("unmarshal receipt result: %w", err)
		}
	}
	return &iam.IAMCommandReceipt{
		OperationID:        row.OperationID.String(),
		CommandName:        row.CommandName,
		RequestFingerprint: row.RequestFingerprint,
		Status:             iam.CommandStatus(row.Status),
		SubjectID:          nullableInt8(row.SubjectID),
		ResultingVersion:   safeVersionOf(safeResult),
		SafeResult:         safeResult,
		ErrorCode:          nullableText(row.ErrorCode),
	}, nil
}

// EnqueueOutboxEvent maps a produced event to the pending outbox row; the
// status/availability/epoch fields default inside the SQL.
func (s *queries) EnqueueOutboxEvent(ctx context.Context, event iam.OutboxEventRecord) error {
	eventID, err := uuidFromString(event.EventID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	return s.q.InsertOutboxEvent(ctx, sqlc.InsertOutboxEventParams{
		EventID:          eventID,
		EventType:        event.EventType,
		EventVersion:     int32(event.EventVersion),
		Producer:         event.Producer,
		AggregateType:    event.AggregateType,
		AggregateID:      event.AggregateID,
		AggregateVersion: event.AggregateVersion,
		Payload:          payload,
		CorrelationID:    event.CorrelationID,
		OccurredAt:       pgtype.Timestamptz{Time: event.OccurredAt, Valid: true},
	})
}

// safeVersionOf extracts the transport-neutral resulting version recorded in
// the receipt result object (the only numeric field the receipts carry today);
// missing or malformed values yield nil — the caller treats it as "no version".
func safeVersionOf(result map[string]any) *int64 {
	v, ok := result["resulting_version"]
	if !ok {
		return nil
	}
	switch n := v.(type) {
	case float64:
		ver := int64(n)
		return &ver
	case json.Number:
		ver, err := n.Int64()
		if err != nil {
			return nil
		}
		return &ver
	}
	return nil
}

// --- helpers -----------------------------------------------------------------

func fullUserRecord(id int64, username, passwordHash string, account, email pgtype.Text,
	state string, version int64, createdAt, updatedAt pgtype.Timestamptz) iam.UserRecord {
	return iam.UserRecord{
		ID:             id,
		Username:       username,
		PasswordHash:   passwordHash,
		Account:        nullableText(account),
		Email:          nullableText(email),
		LifecycleState: iam.LifecycleState(state),
		Version:        version,
		CreatedAt:      createdAt.Time,
		UpdatedAt:      updatedAt.Time,
	}
}

func roleRecord(id int64, name, code string, createdAt pgtype.Timestamptz) iam.RoleRecord {
	return iam.RoleRecord{ID: id, Name: name, Code: code, CreatedAt: createdAt.Time}
}

func nullableText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func nullableInt8(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func int8FromPtr(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func textFromPtr(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}

// uuidFromString parses a UUID text into the pgtype form the generated
// queries use; a malformed operation id is an adapter error, never a query.
func uuidFromString(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid operation id %q: %w", s, err)
	}
	return u, nil
}

func nullableTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// isUniqueViolation reports whether err is a PostgreSQL 23505 unique violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
