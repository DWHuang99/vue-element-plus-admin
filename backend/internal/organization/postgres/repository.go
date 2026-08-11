// Organization PostgreSQL adapter (US1 read side + department ops).
//
// Implements organization.Store against PostgreSQL using ONLY the
// Organization-owned generated sqlc package (boundary rule): no
// global/internal/database/sqlc, no IAM or BFF queries, and every transaction
// touches only Organization tables.
//
// Absence is signalled with the Organization domain errors
// (ErrDepartmentNotFound); unique violations surface as ErrNameTaken.
// Membership absence (no state row) is a normal empty result, not an error.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres/sqlc"
)

// queries carries the query set shared by the pool-bound Store and the
// transaction-bound txStore.
type queries struct {
	q sqlc.Querier
}

// --- departments -------------------------------------------------------------

func (s *queries) ListDepartments(ctx context.Context) ([]organization.Department, error) {
	rows, err := s.q.ListDepartments(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]organization.Department, 0, len(rows))
	for _, r := range rows {
		out = append(out, departmentRecord(r.ID, r.Name, r.ParentID, r.CreatedAt, r.UpdatedAt))
	}
	return out, nil
}

func (s *queries) GetDepartmentByID(ctx context.Context, id int64) (organization.Department, error) {
	row, err := s.q.GetDepartmentByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return organization.Department{}, organization.ErrDepartmentNotFound
		}
		return organization.Department{}, err
	}
	return departmentRecord(row.ID, row.Name, row.ParentID, row.CreatedAt, row.UpdatedAt), nil
}

func (s *queries) GetDepartmentByName(ctx context.Context, name string) (organization.Department, error) {
	row, err := s.q.GetDepartmentByName(ctx, name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return organization.Department{}, organization.ErrDepartmentNotFound
		}
		return organization.Department{}, err
	}
	return departmentRecord(row.ID, row.Name, row.ParentID, row.CreatedAt, row.UpdatedAt), nil
}

func (s *queries) CreateDepartment(ctx context.Context, name string, parentID *int64) (organization.Department, error) {
	row, err := s.q.CreateDepartment(ctx, sqlc.CreateDepartmentParams{Name: name, ParentID: int8FromPtr(parentID)})
	if err != nil {
		if isUniqueViolation(err) {
			return organization.Department{}, organization.ErrNameTaken
		}
		return organization.Department{}, err
	}
	return departmentRecord(row.ID, row.Name, row.ParentID, row.CreatedAt, row.UpdatedAt), nil
}

func (s *queries) UpdateDepartment(ctx context.Context, id int64, name string, parentID *int64) (organization.Department, error) {
	row, err := s.q.UpdateDepartment(ctx, sqlc.UpdateDepartmentParams{
		ID: id, Name: name, ParentID: int8FromPtr(parentID),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return organization.Department{}, organization.ErrNameTaken
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return organization.Department{}, organization.ErrDepartmentNotFound
		}
		return organization.Department{}, err
	}
	return departmentRecord(row.ID, row.Name, row.ParentID, row.CreatedAt, row.UpdatedAt), nil
}

func (s *queries) DeleteDepartment(ctx context.Context, id int64) error {
	return s.q.DeleteDepartment(ctx, id)
}

func (s *queries) CountDepartmentsByParentID(ctx context.Context, parentID int64) (int64, error) {
	return s.q.CountDepartmentsByParentID(ctx, pgtype.Int8{Int64: parentID, Valid: true})
}

func (s *queries) CountUsersByDepartmentID(ctx context.Context, departmentID int64) (int64, error) {
	return s.q.CountUsersByDepartmentID(ctx, pgtype.Int8{Int64: departmentID, Valid: true})
}

// --- membership reads ----------------------------------------------------------

func (s *queries) GetUserMembershipByUserID(ctx context.Context, userID int64) (*organization.MembershipState, error) {
	row, err := s.q.GetUserMembershipByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No state row: "Organization state never established" — the
			// BFF treats this as a normal no-department result.
			return nil, nil
		}
		return nil, err
	}
	m := membershipRecord(row.UserID, row.DepartmentID, row.DepartmentName, row.MembershipVersion,
		row.CreatedAt, row.UpdatedAt)
	return &m, nil
}

func (s *queries) BatchGetUserMemberships(ctx context.Context, userIDs []int64) (map[int64]organization.MembershipState, error) {
	rows, err := s.q.BatchGetUserMemberships(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]organization.MembershipState, len(rows))
	for _, r := range rows {
		out[r.UserID] = membershipRecord(r.UserID, r.DepartmentID, r.DepartmentName, r.MembershipVersion,
			r.CreatedAt, r.UpdatedAt)
	}
	return out, nil
}

func (s *queries) ListUserIDsByDepartment(ctx context.Context, departmentID int64) ([]int64, error) {
	rows, err := s.q.ListUserIDsByDepartment(ctx, pgtype.Int8{Int64: departmentID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out, nil
}

// --- membership mutations (US2/T036) ------------------------------------------

// LockMembershipByUserID takes the row-level lock the service uses for CAS
// decisions. Missing row is a normal empty result (nil), not an error.
func (s *queries) LockMembershipByUserID(ctx context.Context, userID int64) (*organization.MembershipState, error) {
	row, err := s.q.LockMembershipByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	st := membershipRecord(row.UserID, row.DepartmentID, pgtype.Text{}, row.MembershipVersion, row.CreatedAt, row.UpdatedAt)
	return &st, nil
}

func (s *queries) CreateMembership(ctx context.Context, userID int64, departmentID *int64, version int64) (organization.MembershipState, error) {
	row, err := s.q.CreateMembership(ctx, sqlc.CreateMembershipParams{
		UserID: userID, DepartmentID: int8FromPtr(departmentID), MembershipVersion: version,
	})
	if err != nil {
		return organization.MembershipState{}, err
	}
	return membershipRecord(row.UserID, row.DepartmentID, pgtype.Text{}, row.MembershipVersion, row.CreatedAt, row.UpdatedAt), nil
}

func (s *queries) UpdateMembership(ctx context.Context, userID int64, departmentID *int64, version int64) (organization.MembershipState, error) {
	row, err := s.q.UpdateMembership(ctx, sqlc.UpdateMembershipParams{
		UserID: userID, DepartmentID: int8FromPtr(departmentID), MembershipVersion: version,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The row was locked in the same transaction, so it cannot
			// vanish here; a miss means an adapter bug.
			return organization.MembershipState{}, organization.Wrap(organization.KindInternal, "membership row vanished under lock")
		}
		return organization.MembershipState{}, err
	}
	return membershipRecord(row.UserID, row.DepartmentID, pgtype.Text{}, row.MembershipVersion, row.CreatedAt, row.UpdatedAt), nil
}

// --- command receipts (T038) --------------------------------------------------

func (s *queries) SaveCommandReceipt(ctx context.Context, receipt organization.OrganizationCommandReceipt) error {
	operationID, err := uuidFromString(receipt.OperationID)
	if err != nil {
		return err
	}
	return s.q.SaveCommandReceipt(ctx, sqlc.SaveCommandReceiptParams{
		OperationID:                operationID,
		CommandName:                receipt.CommandName,
		RequestFingerprint:         receipt.RequestFingerprint,
		Status:                     string(receipt.Status),
		SubjectID:                  int8FromPtr(receipt.SubjectID),
		PreviousDepartmentID:       int8FromPtr(receipt.PreviousDepartmentID),
		PreviousMembershipVersion:  int8FromPtr(receipt.PreviousMembershipVersion),
		ResultingDepartmentID:      int8FromPtr(receipt.ResultingDepartmentID),
		ResultingMembershipVersion: int8FromPtr(receipt.ResultingMembershipVersion),
		ErrorCode:                  textFromPtr(receipt.ErrorCode),
	})
}

func (s *queries) GetCommandReceipt(ctx context.Context, operationID, commandName string) (*organization.OrganizationCommandReceipt, error) {
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
	return &organization.OrganizationCommandReceipt{
		OperationID:                row.OperationID.String(),
		CommandName:                row.CommandName,
		RequestFingerprint:         row.RequestFingerprint,
		Status:                     organization.CommandStatus(row.Status),
		SubjectID:                  nullableInt8(row.SubjectID),
		PreviousDepartmentID:       nullableInt8(row.PreviousDepartmentID),
		PreviousMembershipVersion:  nullableInt8(row.PreviousMembershipVersion),
		ResultingDepartmentID:      nullableInt8(row.ResultingDepartmentID),
		ResultingMembershipVersion: nullableInt8(row.ResultingMembershipVersion),
		ErrorCode:                  nullableText(row.ErrorCode),
	}, nil
}

// --- terminal user-deleted consumption (T039/T040) -----------------------------

func (s *queries) DeleteMembershipsForUsers(ctx context.Context, userIDs []int64) error {
	return s.q.DeleteMembershipsForUsers(ctx, userIDs)
}

func (s *queries) InboxMessageExists(ctx context.Context, eventID, handlerName string) (bool, error) {
	parsed, err := uuidFromString(eventID)
	if err != nil {
		return false, err
	}
	_, err = s.q.GetInboxMessage(ctx, sqlc.GetInboxMessageParams{
		EventID: parsed, HandlerName: handlerName,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *queries) SaveInboxMessage(ctx context.Context, eventID, handlerName, eventType string, eventVersion int, aggregateID string, aggregateVersion int64) error {
	parsed, err := uuidFromString(eventID)
	if err != nil {
		return err
	}
	return s.q.InsertInboxMessage(ctx, sqlc.InsertInboxMessageParams{
		EventID: parsed, HandlerName: handlerName, EventType: eventType,
		EventVersion: int32(eventVersion), AggregateID: aggregateID, AggregateVersion: aggregateVersion,
	})
}

func (s *queries) IncrementInboxMetric(ctx context.Context, metricKey string, delta int64) error {
	return s.q.IncrementInboxMetric(ctx, sqlc.IncrementInboxMetricParams{
		MetricKey: metricKey, Count: delta,
	})
}

// --- helpers ------------------------------------------------------------------

// uuidFromString parses UUID text for receipt keys; an invalid shape is
// invalid input, not a driver error.
func uuidFromString(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, organization.Wrap(organization.KindInvalidInput, "invalid operation id: "+s)
	}
	return u, nil
}

func textFromPtr(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}

func departmentRecord(id int64, name string, parentID pgtype.Int8,
	createdAt, updatedAt pgtype.Timestamptz) organization.Department {
	return organization.Department{
		ID:        id,
		Name:      name,
		ParentID:  nullableInt8(parentID),
		CreatedAt: createdAt.Time,
		UpdatedAt: updatedAt.Time,
	}
}

func membershipRecord(userID int64, departmentID pgtype.Int8, departmentName pgtype.Text,
	version int64, createdAt, updatedAt pgtype.Timestamptz) organization.MembershipState {
	return organization.MembershipState{
		UserID:            userID,
		DepartmentID:      nullableInt8(departmentID),
		DepartmentName:    nullableText(departmentName),
		MembershipVersion: version,
		CreatedAt:         createdAt.Time,
		UpdatedAt:         updatedAt.Time,
	}
}

func nullableInt8(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// int8FromPtr converts a domain pointer back to a pgtype.Int8 parameter.
func int8FromPtr(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func nullableText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// isUniqueViolation reports whether err is a PostgreSQL 23505 unique violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
