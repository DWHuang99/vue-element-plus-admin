// Admin BFF application service: cross-domain composition over the IAM and
// Organization ports (T028 /auth/me, T029 user list). Pure orchestration —
// no pgx/sqlc/migrations, no adapter types, no HTTP. The transport layer maps
// these results to the frozen public DTOs (transport/http/dto.go).
package adminbff

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
)

// --- composition results (application types, transport maps to DTOs) ---------

// DepartmentBrief is the public department reference.
type DepartmentBrief struct {
	ID   int64
	Name string
}

// RoleBrief is the public role reference inside a profile.
type RoleBrief struct {
	ID   int64
	Name string
	Code string
}

// UserProfile is the aggregated IAM + Organization profile.
type UserProfile struct {
	ID                   int64
	Username             string
	Account              *string
	Email                *string
	CreatedAt            time.Time
	Department           *DepartmentBrief // nil = no membership (JSON null)
	Roles                []RoleBrief
	EffectivePermissions []string // non-nil, sorted unique
}

// MeResult is the /auth/me composition payload.
type MeResult struct {
	User UserProfile
}

// UserListItem is one managed-user list row. Role is the comma-separated role
// name string for current frontend compatibility.
type UserListItem struct {
	ID         int64
	Username   string
	Account    *string
	Email      *string
	CreateTime time.Time
	Role       string
	Department *DepartmentBrief // nil = no membership (JSON null)
}

// UserListResult is the paginated managed-user composition payload.
type UserListResult struct {
	List  []UserListItem
	Total int64
}

// ListManagedUsersParams mirrors the public GET /users query set.
type ListManagedUsersParams struct {
	DepartmentID *int64
	Username     string
	Account      string
	PageIndex    int
	PageSize     int
}

// --- service -----------------------------------------------------------------

// Service composes the IAM and Organization participants over the durable
// workflow store. The IAM half owns identity/roles/permissions; the
// Organization half owns department and membership projections; the
// workflow store owns the saga state machine (T052+). Neither side's
// internals leak into the BFF.
type Service struct {
	iam  IAMParticipants
	org  OrganizationParticipants
	work WorkflowStore
	log  *slog.Logger
}

// NewService wires the composition to its participants and the workflow
// store.
func NewService(iamParts IAMParticipants, orgParts OrganizationParticipants, work WorkflowStore, logger *slog.Logger) *Service {
	return &Service{iam: iamParts, org: orgParts, work: work, log: logger}
}

// Me composes the aggregated profile (T028): IAM GetIdentity +
// GetAuthorizationProfile, then Organization GetUserDepartment. An
// Organization dependency failure surfaces as the stable
// ErrDependencyUnavailable with the correlation ID logged — never as a
// fabricated fresh-looking profile.
func (s *Service) Me(ctx context.Context, userID int64, correlationID string) (MeResult, error) {
	identity, err := s.iam.Identity.GetIdentity(ctx, userID)
	if err != nil {
		return MeResult{}, err
	}
	authz, err := s.iam.Identity.GetAuthorizationProfile(ctx, userID)
	if err != nil {
		return MeResult{}, err
	}
	membership, err := s.org.Membership.GetUserDepartment(ctx, userID)
	if err != nil {
		s.log.Error("organization dependency unavailable during profile composition",
			"correlation_id", correlationID,
			"user_id", userID,
			"error", err)
		return MeResult{}, ErrDependencyUnavailable
	}

	profile := UserProfile{
		ID:                   identity.ID,
		Username:             identity.Username,
		Account:              identity.Account,
		Email:                identity.Email,
		CreatedAt:            identity.CreatedAt,
		Roles:                roleBriefs(authz.Roles),
		EffectivePermissions: authz.EffectivePermissions,
	}
	if membership != nil && membership.DepartmentID != nil && membership.DepartmentName != nil {
		profile.Department = &DepartmentBrief{ID: *membership.DepartmentID, Name: *membership.DepartmentName}
	}
	return MeResult{User: profile}, nil
}

// ListManagedUsers is the paginated managed-user composition (T029):
//
//   - no department filter: IAM filters and paginates over all managed users
//     (nil candidate list), Organization batch-resolves the current page;
//   - department filter: Organization ListUserIDsByDepartment narrows the
//     candidates first, then IAM filters/paginates inside that set.
//
// Filtering always precedes pagination; the call count is bounded (2-3
// queries total, no N+1) and the Organization batch lookup covers only the
// page items.
func (s *Service) ListManagedUsers(ctx context.Context, p ListManagedUsersParams) (UserListResult, error) {
	var candidates []int64 // nil = all managed users (IAM semantics)
	if p.DepartmentID != nil {
		ids, err := s.org.Membership.ListUserIDsByDepartment(ctx, *p.DepartmentID)
		if err != nil {
			return UserListResult{}, err
		}
		candidates = ids
	}

	page, err := s.iam.Identity.BatchGetManagedIdentities(
		ctx, candidates, filterPtr(p.Username), filterPtr(p.Account), p.PageIndex, p.PageSize)
	if err != nil {
		return UserListResult{}, err
	}

	items := make([]UserListItem, 0, len(page.Items))
	if len(page.Items) > 0 {
		ids := make([]int64, 0, len(page.Items))
		for _, m := range page.Items {
			ids = append(ids, m.ID)
		}
		depts, err := s.org.Membership.BatchGetUserDepartments(ctx, ids)
		if err != nil {
			return UserListResult{}, err
		}
		for _, m := range page.Items {
			item := UserListItem{
				ID:         m.ID,
				Username:   m.Username,
				Account:    m.Account,
				Email:      m.Email,
				CreateTime: m.CreatedAt,
				Role:       roleNames(m.Roles),
			}
			if state, ok := depts[m.ID]; ok && state.DepartmentID != nil && state.DepartmentName != nil {
				item.Department = &DepartmentBrief{ID: *state.DepartmentID, Name: *state.DepartmentName}
			}
			items = append(items, item)
		}
	}
	return UserListResult{List: items, Total: page.Total}, nil
}

// --- helpers -----------------------------------------------------------------

// filterPtr converts an empty public filter to nil (IAM treats nil and empty
// equivalently; nil keeps the "no filter" semantics explicit).
func filterPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// roleBriefs maps IAM role summaries; empty is a non-nil empty slice.
func roleBriefs(roles []iam.RoleSummary) []RoleBrief {
	out := make([]RoleBrief, 0, len(roles))
	for _, r := range roles {
		out = append(out, RoleBrief{ID: r.ID, Name: r.Name, Code: r.Code})
	}
	return out
}

// roleNames joins role names for the legacy-compatible comma string.
func roleNames(roles []iam.RoleSummary) string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return strings.Join(names, ",")
}
