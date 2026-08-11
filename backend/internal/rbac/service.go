//go:build rollback

// Package rbac provides department/role/user management for the admin console.
// Layered like internal/auth: business rules live in the service (testable),
// the handler only adapts HTTP to the service (see constitution principle IV).
package rbac

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/auth"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

// RoleView is a role as exposed over the API.
type RoleView struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	IsBuiltin bool      `json:"is_builtin"`
	CreatedAt time.Time `json:"created_at"`
}

// DepartmentView is a department tree node.
type DepartmentView struct {
	ID       int64            `json:"id"`
	Name     string           `json:"name"`
	ParentID *int64           `json:"parent_id"`
	Children []DepartmentView `json:"children"`
}

// DepartmentBrief is a lightweight department reference (used in user rows).
type DepartmentBrief struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// UserListView is a single row of the paginated user list.
// Role is the joined role-name string; Department is the department brief.
type UserListView struct {
	ID         int64            `json:"id"`
	Username   string           `json:"username"`
	Account    string           `json:"account"`
	Email      string           `json:"email"`
	CreateTime time.Time        `json:"create_time"`
	Role       string           `json:"role"`
	Department *DepartmentBrief `json:"department"`
}

// UserListResult is the paginated user list payload.
type UserListResult struct {
	List  []UserListView `json:"list"`
	Total int64          `json:"total"`
}

// SaveRoleParams carries a create-or-update role operation.
type SaveRoleParams struct {
	ID   *int64
	Name string
	Code string
}

// SaveDepartmentParams carries a create-or-update department operation.
type SaveDepartmentParams struct {
	ID       *int64
	Name     string
	ParentID *int64
}

// ListUsersParams carries the pagination/filter options for the user list.
type ListUsersParams struct {
	DepartmentID int64
	Username     string
	Account      string
	PageIndex    int
	PageSize     int
}

// SaveUserParams carries a create-or-update user operation.
// Password is required on create and ignored (kept) on update when nil.
type SaveUserParams struct {
	ID           *int64
	Username     string
	Account      string
	Email        string
	Password     *string
	DepartmentID *int64
	Roles        []int64
}

// UsersDeleteDelegate (T076) is the US5 delegation target for the legacy
// POST /users/delete route: when installed, the handler calls the IAM
// DeleteUsers port (outbox event + receipt) instead of the legacy direct
// delete. The extra actor/correlation arguments come from the legacy auth
// middleware context so the IAM operation audit keeps the acting principal.
// Declared as a duck interface so the legacy module stays IAM-free — the
// composition root supplies an iam.Service-backed adapter.
type UsersDeleteDelegate interface {
	DeleteUsers(ctx context.Context, actorID int64, correlationID string, ids []int64) error
}

// Service is the rbac business logic boundary (independent, testable).
type Service interface {
	ListRoles(ctx context.Context) ([]RoleView, error)
	SaveRole(ctx context.Context, params SaveRoleParams) error
	DeleteRoles(ctx context.Context, ids []int64) error

	ListDepartments(ctx context.Context) ([]DepartmentView, error)
	SaveDepartment(ctx context.Context, params SaveDepartmentParams) error
	DeleteDepartments(ctx context.Context, ids []int64) error

	ListUsers(ctx context.Context, params ListUsersParams) (*UserListResult, error)
	SaveUser(ctx context.Context, params SaveUserParams) error
	DeleteUsers(ctx context.Context, ids []int64) error
}

// RBACService implements Service against PostgreSQL via sqlc.
type RBACService struct {
	pool *pgxpool.Pool
	q    sqlc.Querier
}

// NewRBACService creates an RBACService.
func NewRBACService(pool *pgxpool.Pool) *RBACService {
	return &RBACService{pool: pool, q: sqlc.New(pool)}
}

// --- roles ---

// ListRoles returns all roles ordered by id.
func (s *RBACService) ListRoles(ctx context.Context) ([]RoleView, error) {
	rows, err := s.q.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	views := make([]RoleView, 0, len(rows))
	for _, r := range rows {
		views = append(views, RoleView{
			ID:        r.ID,
			Name:      r.Name,
			Code:      r.Code,
			IsBuiltin: isBuiltinRoleCode(r.Code),
			CreatedAt: r.CreatedAt.Time,
		})
	}
	return views, nil
}

// SaveRole creates (no id) or updates (with id) a role.
func (s *RBACService) SaveRole(ctx context.Context, p SaveRoleParams) error {
	if p.ID != nil {
		existing, err := s.q.GetRoleByID(ctx, *p.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRoleNotFound
			}
			return fmt.Errorf("check role: %w", err)
		}
		if isBuiltinRoleCode(existing.Code) && p.Code != existing.Code {
			return ErrBuiltinRoleCodeImmutable
		}
		if _, err := s.q.UpdateRole(ctx, sqlc.UpdateRoleParams{ID: *p.ID, Name: p.Name, Code: p.Code}); err != nil {
			return mapUniqueViolation(err)
		}
		return nil
	}
	if _, err := s.q.CreateRole(ctx, sqlc.CreateRoleParams{Name: p.Name, Code: p.Code}); err != nil {
		return mapUniqueViolation(err)
	}
	return nil
}

// DeleteRoles removes roles atomically, refusing built-ins and roles still
// referenced by users. The full batch is validated before any row is deleted.
func (s *RBACService) DeleteRoles(ctx context.Context, ids []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete roles transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := sqlc.New(tx)
	for _, id := range ids {
		role, err := qtx.GetRoleByID(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRoleNotFound
			}
			return fmt.Errorf("check role: %w", err)
		}
		if isBuiltinRoleCode(role.Code) {
			return ErrBuiltinRoleDeleteProtected
		}
		n, err := qtx.CountUserRolesByRoleID(ctx, id)
		if err != nil {
			return fmt.Errorf("count role users: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("%w: role %d still has users", ErrDeleteProtected, id)
		}
	}

	for _, id := range ids {
		if err := qtx.DeleteRole(ctx, id); err != nil {
			return fmt.Errorf("delete role: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete roles transaction: %w", err)
	}
	return nil
}

// --- departments ---

// ListDepartments returns the department tree (roots with nested children).
func (s *RBACService) ListDepartments(ctx context.Context) ([]DepartmentView, error) {
	rows, err := s.q.ListDepartments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list departments: %w", err)
	}
	nodes := make(map[int64]*DepartmentView, len(rows))
	for _, r := range rows {
		var parentID *int64
		if r.ParentID.Valid {
			parentID = &r.ParentID.Int64
		}
		nodes[r.ID] = &DepartmentView{ID: r.ID, Name: r.Name, ParentID: parentID, Children: []DepartmentView{}}
	}
	for _, r := range rows {
		node := nodes[r.ID]
		if r.ParentID.Valid {
			if parent, ok := nodes[r.ParentID.Int64]; ok {
				parent.Children = append(parent.Children, *node)
			}
		}
	}
	roots := make([]DepartmentView, 0)
	for _, r := range rows {
		if !r.ParentID.Valid {
			roots = append(roots, *nodes[r.ID])
		}
	}
	return roots, nil
}

// SaveDepartment creates (no id) or updates (with id) a department.
func (s *RBACService) SaveDepartment(ctx context.Context, p SaveDepartmentParams) error {
	if p.ParentID != nil && p.ID != nil && *p.ParentID == *p.ID {
		return ErrInvalidInput
	}
	if p.ID != nil {
		if _, err := s.q.GetDepartmentByID(ctx, *p.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDepartmentNotFound
			}
			return fmt.Errorf("check department: %w", err)
		}
		if _, err := s.q.UpdateDepartment(ctx, sqlc.UpdateDepartmentParams{
			ID:       *p.ID,
			Name:     p.Name,
			ParentID: pgint8p(p.ParentID),
		}); err != nil {
			return mapUniqueViolation(err)
		}
		return nil
	}
	if _, err := s.q.CreateDepartment(ctx, sqlc.CreateDepartmentParams{
		Name:     p.Name,
		ParentID: pgint8p(p.ParentID),
	}); err != nil {
		return mapUniqueViolation(err)
	}
	return nil
}

// DeleteDepartments removes departments atomically, refusing ones with
// children or users. The full batch is validated before any row is deleted.
func (s *RBACService) DeleteDepartments(ctx context.Context, ids []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete departments transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := sqlc.New(tx)
	for _, id := range ids {
		if _, err := qtx.GetDepartmentByID(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDepartmentNotFound
			}
			return fmt.Errorf("check department: %w", err)
		}
		children, err := qtx.CountDepartmentsByParentID(ctx, pgtype.Int8{Int64: id, Valid: true})
		if err != nil {
			return fmt.Errorf("count children: %w", err)
		}
		if children > 0 {
			return fmt.Errorf("%w: department %d has children", ErrDeleteProtected, id)
		}
		users, err := qtx.CountUsersByDepartmentID(ctx, pgtype.Int8{Int64: id, Valid: true})
		if err != nil {
			return fmt.Errorf("count department users: %w", err)
		}
		if users > 0 {
			return fmt.Errorf("%w: department %d has users", ErrDeleteProtected, id)
		}
	}

	for _, id := range ids {
		if err := qtx.DeleteDepartment(ctx, id); err != nil {
			return fmt.Errorf("delete department: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete departments transaction: %w", err)
	}
	return nil
}

// --- users ---

// ListUsers returns a paginated, optionally filtered user list. Every row is
// enriched with its role-name string and department brief.
func (s *RBACService) ListUsers(ctx context.Context, p ListUsersParams) (*UserListResult, error) {
	pageSize := p.PageSize
	if pageSize < 1 {
		pageSize = 10
	}
	pageSize = min(pageSize, 100)
	pageIndex := max(p.PageIndex, 1)
	limit := int32(pageSize)
	offset := int32((pageIndex - 1) * pageSize)

	params := sqlc.ListUsersByDepartmentParams{
		Column1: p.DepartmentID,
		Column2: p.Username,
		Column3: p.Account,
		Limit:   limit,
		Offset:  offset,
	}
	rows, err := s.q.ListUsersByDepartment(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	total, err := s.q.CountUsersByDepartment(ctx, sqlc.CountUsersByDepartmentParams{
		Column1: p.DepartmentID,
		Column2: p.Username,
		Column3: p.Account,
	})
	if err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}

	list := make([]UserListView, 0, len(rows))
	for _, row := range rows {
		item := UserListView{
			ID:         row.ID,
			Username:   row.Username,
			Account:    pgtextStr(row.Account),
			Email:      pgtextStr(row.Email),
			CreateTime: row.CreatedAt.Time,
		}
		roleRows, err := s.q.ListRolesByUserID(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("list user roles: %w", err)
		}
		names := make([]string, 0, len(roleRows))
		for _, rr := range roleRows {
			names = append(names, rr.Name)
		}
		item.Role = strings.Join(names, ",")

		if row.DepartmentID.Valid {
			dept, err := s.q.GetDepartmentByID(ctx, row.DepartmentID.Int64)
			if err == nil {
				item.Department = &DepartmentBrief{ID: dept.ID, Name: dept.Name}
			}
		}
		list = append(list, item)
	}
	return &UserListResult{List: list, Total: total}, nil
}

// SaveUser creates or updates a user and replaces their role set atomically.
func (s *RBACService) SaveUser(ctx context.Context, p SaveUserParams) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := sqlc.New(tx)

	var userID int64
	if p.ID != nil {
		existing, err := qtx.GetUserByID(ctx, *p.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUserNotFound
			}
			return fmt.Errorf("check user: %w", err)
		}
		userID = existing.ID
		if p.Password != nil {
			hash, err := auth.HashPassword(*p.Password)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}
			if err := qtx.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{ID: userID, PasswordHash: hash}); err != nil {
				return fmt.Errorf("update password: %w", err)
			}
		}
		if _, err := qtx.UpdateRbacUser(ctx, sqlc.UpdateRbacUserParams{
			ID:           userID,
			Account:      pgtext(p.Account),
			Email:        pgtext(p.Email),
			DepartmentID: pgint8p(p.DepartmentID),
		}); err != nil {
			return fmt.Errorf("update user: %w", err)
		}
	} else {
		if p.Password == nil {
			return ErrInvalidInput
		}
		hash, err := auth.HashPassword(*p.Password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		user, err := qtx.CreateRbacUser(ctx, sqlc.CreateRbacUserParams{
			Username:     p.Username,
			PasswordHash: hash,
			Account:      pgtext(p.Account),
			Email:        pgtext(p.Email),
			DepartmentID: pgint8p(p.DepartmentID),
		})
		if err != nil {
			return mapUniqueViolation(err)
		}
		userID = user.ID
	}

	// Validate role references up-front for clean 404s, then replace the set.
	for _, roleID := range p.Roles {
		if _, err := qtx.GetRoleByID(ctx, roleID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRoleNotFound
			}
			return fmt.Errorf("check role: %w", err)
		}
	}
	if err := qtx.DeleteUserRolesByUserID(ctx, userID); err != nil {
		return fmt.Errorf("clear roles: %w", err)
	}
	for _, roleID := range p.Roles {
		if err := qtx.InsertUserRole(ctx, sqlc.InsertUserRoleParams{UserID: userID, RoleID: roleID}); err != nil {
			return fmt.Errorf("assign role: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// DeleteUsers physically deletes users atomically; user_roles cascade away.
// The full batch is validated before any row is deleted.
func (s *RBACService) DeleteUsers(ctx context.Context, ids []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete users transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := sqlc.New(tx)
	for _, id := range ids {
		if _, err := qtx.GetUserByID(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUserNotFound
			}
			return fmt.Errorf("check user: %w", err)
		}
	}

	for _, id := range ids {
		if err := qtx.DeleteUser(ctx, id); err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete users transaction: %w", err)
	}
	return nil
}

// --- helpers ---

func isBuiltinRoleCode(code string) bool {
	switch code {
	case "super_admin", "admin", "user":
		return true
	default:
		return false
	}
}

// pgint8p maps a *int64 to pgtype.Int8 (nil -> NULL).
func pgint8p(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

// pgtext maps a string to pgtype.Text (empty -> NULL).
func pgtext(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// pgtextStr renders a nullable text column as a plain string (NULL -> "").
func pgtextStr(t pgtype.Text) string {
	if t.Valid {
		return t.String
	}
	return ""
}

// mapUniqueViolation converts a PG unique-violation into ErrNameTaken.
func mapUniqueViolation(err error) error {
	if isUniqueViolation(err) {
		return ErrNameTaken
	}
	return err
}

// isUniqueViolation reports whether err is a PostgreSQL 23505 unique violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
