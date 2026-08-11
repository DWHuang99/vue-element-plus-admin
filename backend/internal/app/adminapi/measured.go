// Measured port decorators (T074): every application-port method the BFF
// composes is wrapped so the composition root records per-port call counts,
// latency samples and typed error classes without touching the services.
//
// Decorators implement every interface method explicitly — no interface
// embedding — so a port gaining a method fails to compile here instead of
// silently bypassing measurement. Only durations and error Kinds are recorded;
// payloads (including passwords and tokens) never enter the registry.
package adminapi

import (
	"context"
	"errors"
	"time"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

// measure records one completed port call: calls_total, one latency sample
// and, on error, errors_total plus the typed class port_<port>_errors_<kind>_total.
func measure(reg *observability.Registry, port string, start time.Time, err error) {
	if reg == nil {
		return
	}
	ms := float64(time.Since(start).Microseconds()) / 1000.0
	reg.AddCounter("port_"+port+"_calls_total", 1)
	reg.RecordDuration("port_"+port+"_latency_ms", ms)
	if err != nil {
		reg.AddCounter("port_"+port+"_errors_total", 1)
		reg.AddCounter("port_"+port+"_errors_"+classifyError(err)+"_total", 1)
	}
}

// classifyError maps an error to a stable class label: the domain Kind for
// IAM/Organization errors (never the message — messages can carry values),
// "internal" for everything else.
func classifyError(err error) string {
	var ie *iam.Error
	if errors.As(err, &ie) {
		return string(ie.Kind)
	}
	var oe *organization.Error
	if errors.As(err, &oe) {
		return string(oe.Kind)
	}
	return "internal"
}

// measuredAuth decorates iam.AuthService (port "auth").
type measuredAuth struct {
	inner iam.AuthService
	reg   *observability.Registry
}

func (m *measuredAuth) Register(ctx context.Context, username, password string, requestCtx iam.OperationContext) (iam.AuthSession, error) {
	start := time.Now()
	s, err := m.inner.Register(ctx, username, password, requestCtx)
	measure(m.reg, "auth", start, err)
	return s, err
}

func (m *measuredAuth) Login(ctx context.Context, username, password string, requestCtx iam.OperationContext) (iam.AuthSession, error) {
	start := time.Now()
	s, err := m.inner.Login(ctx, username, password, requestCtx)
	measure(m.reg, "auth", start, err)
	return s, err
}

func (m *measuredAuth) RevokeSession(ctx context.Context, rawToken string, requestCtx iam.OperationContext) error {
	start := time.Now()
	err := m.inner.RevokeSession(ctx, rawToken, requestCtx)
	measure(m.reg, "auth", start, err)
	return err
}

func (m *measuredAuth) Authenticate(ctx context.Context, rawToken string, requestCtx iam.OperationContext) (iam.Principal, error) {
	start := time.Now()
	p, err := m.inner.Authenticate(ctx, rawToken, requestCtx)
	measure(m.reg, "auth", start, err)
	return p, err
}

// measuredIdentity decorates iam.IdentityService (port "identity").
type measuredIdentity struct {
	inner iam.IdentityService
	reg   *observability.Registry
}

func (m *measuredIdentity) GetIdentity(ctx context.Context, userID int64) (iam.IdentityProfile, error) {
	start := time.Now()
	p, err := m.inner.GetIdentity(ctx, userID)
	measure(m.reg, "identity", start, err)
	return p, err
}

func (m *measuredIdentity) GetAuthorizationProfile(ctx context.Context, userID int64) (iam.AuthorizationProfile, error) {
	start := time.Now()
	p, err := m.inner.GetAuthorizationProfile(ctx, userID)
	measure(m.reg, "identity", start, err)
	return p, err
}

func (m *measuredIdentity) HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error) {
	start := time.Now()
	ok, err := m.inner.HasPermission(ctx, userID, permissionCode)
	measure(m.reg, "identity", start, err)
	return ok, err
}

func (m *measuredIdentity) BatchGetManagedIdentities(ctx context.Context, candidateUserIDs []int64,
	usernameFilter, accountFilter *string, pageIndex, pageSize int) (iam.ManagedIdentityPage, error) {
	start := time.Now()
	p, err := m.inner.BatchGetManagedIdentities(ctx, candidateUserIDs, usernameFilter, accountFilter, pageIndex, pageSize)
	measure(m.reg, "identity", start, err)
	return p, err
}

// measuredRoles decorates iam.RoleService (port "roles").
type measuredRoles struct {
	inner iam.RoleService
	reg   *observability.Registry
}

func (m *measuredRoles) ListRoles(ctx context.Context) ([]iam.RoleSummary, error) {
	start := time.Now()
	rs, err := m.inner.ListRoles(ctx)
	measure(m.reg, "roles", start, err)
	return rs, err
}

func (m *measuredRoles) ValidateRoleIDs(ctx context.Context, ids []int64) ([]iam.RoleSummary, error) {
	start := time.Now()
	rs, err := m.inner.ValidateRoleIDs(ctx, ids)
	measure(m.reg, "roles", start, err)
	return rs, err
}

func (m *measuredRoles) SaveRole(ctx context.Context, requestCtx iam.OperationContext, id *int64, name, code string) (int64, error) {
	start := time.Now()
	rid, err := m.inner.SaveRole(ctx, requestCtx, id, name, code)
	measure(m.reg, "roles", start, err)
	return rid, err
}

func (m *measuredRoles) DeleteRoles(ctx context.Context, requestCtx iam.OperationContext, ids []int64) error {
	start := time.Now()
	err := m.inner.DeleteRoles(ctx, requestCtx, ids)
	measure(m.reg, "roles", start, err)
	return err
}

// measuredManaged decorates iam.ManagedUserService (port "managed_users").
type measuredManaged struct {
	inner iam.ManagedUserService
	reg   *observability.Registry
}

func (m *measuredManaged) CreateProvisioningUser(ctx context.Context, requestCtx iam.OperationContext,
	username string, account, email *string, password string, roleIDs []int64) (iam.CreateUserResult, error) {
	start := time.Now()
	r, err := m.inner.CreateProvisioningUser(ctx, requestCtx, username, account, email, password, roleIDs)
	measure(m.reg, "managed_users", start, err)
	return r, err
}

func (m *measuredManaged) ActivateUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64) (int64, error) {
	start := time.Now()
	v, err := m.inner.ActivateUser(ctx, requestCtx, userID, expectedVersion)
	measure(m.reg, "managed_users", start, err)
	return v, err
}

func (m *measuredManaged) DisableUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64, reasonCode string) (int64, error) {
	start := time.Now()
	v, err := m.inner.DisableUser(ctx, requestCtx, userID, expectedVersion, reasonCode)
	measure(m.reg, "managed_users", start, err)
	return v, err
}

func (m *measuredManaged) UpdateManagedUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64,
	username string, account, email *string, password *string, roleIDs []int64) (int64, error) {
	start := time.Now()
	v, err := m.inner.UpdateManagedUser(ctx, requestCtx, userID, expectedVersion, username, account, email, password, roleIDs)
	measure(m.reg, "managed_users", start, err)
	return v, err
}

func (m *measuredManaged) DeleteUsers(ctx context.Context, requestCtx iam.OperationContext, targets []iam.DeleteTarget) ([]iam.BatchDeleteResultItem, error) {
	start := time.Now()
	r, err := m.inner.DeleteUsers(ctx, requestCtx, targets)
	measure(m.reg, "managed_users", start, err)
	return r, err
}

func (m *measuredManaged) CompensateProvisioningUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64) error {
	start := time.Now()
	err := m.inner.CompensateProvisioningUser(ctx, requestCtx, userID, expectedVersion)
	measure(m.reg, "managed_users", start, err)
	return err
}

// measuredIAMReceipts decorates iam.ReceiptResolver (port "iam_receipts").
type measuredIAMReceipts struct {
	inner iam.ReceiptResolver
	reg   *observability.Registry
}

func (m *measuredIAMReceipts) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*iam.IAMCommandReceipt, error) {
	start := time.Now()
	r, err := m.inner.ResolveCommand(ctx, operationID, commandName, expectedFingerprint)
	measure(m.reg, "iam_receipts", start, err)
	return r, err
}

// measuredDepartments decorates organization.DepartmentService (port "departments").
type measuredDepartments struct {
	inner organization.DepartmentService
	reg   *observability.Registry
}

func (m *measuredDepartments) ListDepartmentTree(ctx context.Context) ([]organization.DepartmentNode, error) {
	start := time.Now()
	t, err := m.inner.ListDepartmentTree(ctx)
	measure(m.reg, "departments", start, err)
	return t, err
}

func (m *measuredDepartments) GetDepartment(ctx context.Context, departmentID int64) (organization.Department, error) {
	start := time.Now()
	d, err := m.inner.GetDepartment(ctx, departmentID)
	measure(m.reg, "departments", start, err)
	return d, err
}

func (m *measuredDepartments) ValidateDepartment(ctx context.Context, departmentID *int64) (*organization.Department, error) {
	start := time.Now()
	d, err := m.inner.ValidateDepartment(ctx, departmentID)
	measure(m.reg, "departments", start, err)
	return d, err
}

func (m *measuredDepartments) SaveDepartment(ctx context.Context, requestCtx organization.OperationContext, id *int64, name string, parentID *int64) (int64, error) {
	start := time.Now()
	did, err := m.inner.SaveDepartment(ctx, requestCtx, id, name, parentID)
	measure(m.reg, "departments", start, err)
	return did, err
}

func (m *measuredDepartments) DeleteDepartments(ctx context.Context, requestCtx organization.OperationContext, ids []int64) error {
	start := time.Now()
	err := m.inner.DeleteDepartments(ctx, requestCtx, ids)
	measure(m.reg, "departments", start, err)
	return err
}

// measuredMembership decorates organization.MembershipService (port "membership").
type measuredMembership struct {
	inner organization.MembershipService
	reg   *observability.Registry
}

func (m *measuredMembership) GetUserDepartment(ctx context.Context, userID int64) (*organization.MembershipState, error) {
	start := time.Now()
	s, err := m.inner.GetUserDepartment(ctx, userID)
	measure(m.reg, "membership", start, err)
	return s, err
}

func (m *measuredMembership) BatchGetUserDepartments(ctx context.Context, userIDs []int64) (map[int64]organization.MembershipState, error) {
	start := time.Now()
	s, err := m.inner.BatchGetUserDepartments(ctx, userIDs)
	measure(m.reg, "membership", start, err)
	return s, err
}

func (m *measuredMembership) ListUserIDsByDepartment(ctx context.Context, departmentID int64) ([]int64, error) {
	start := time.Now()
	ids, err := m.inner.ListUserIDsByDepartment(ctx, departmentID)
	measure(m.reg, "membership", start, err)
	return ids, err
}

func (m *measuredMembership) SetUserDepartment(ctx context.Context, requestCtx organization.OperationContext,
	userID, departmentID int64, expectedMembershipVersion *int64) (organization.MembershipMutationResult, error) {
	start := time.Now()
	r, err := m.inner.SetUserDepartment(ctx, requestCtx, userID, departmentID, expectedMembershipVersion)
	measure(m.reg, "membership", start, err)
	return r, err
}

func (m *measuredMembership) ClearUserDepartment(ctx context.Context, requestCtx organization.OperationContext,
	userID int64, expectedMembershipVersion *int64) (organization.MembershipMutationResult, error) {
	start := time.Now()
	r, err := m.inner.ClearUserDepartment(ctx, requestCtx, userID, expectedMembershipVersion)
	measure(m.reg, "membership", start, err)
	return r, err
}

func (m *measuredMembership) RestoreUserDepartment(ctx context.Context, requestCtx organization.OperationContext,
	userID int64, expectedCurrentMembershipVersion int64,
	previousDepartmentID *int64, previousMembershipVersion *int64) (organization.MembershipMutationResult, error) {
	start := time.Now()
	r, err := m.inner.RestoreUserDepartment(ctx, requestCtx, userID, expectedCurrentMembershipVersion,
		previousDepartmentID, previousMembershipVersion)
	measure(m.reg, "membership", start, err)
	return r, err
}

func (m *measuredMembership) ClearMembershipsForUsers(ctx context.Context, requestCtx organization.OperationContext, userIDs []int64) error {
	start := time.Now()
	err := m.inner.ClearMembershipsForUsers(ctx, requestCtx, userIDs)
	measure(m.reg, "membership", start, err)
	return err
}

// measuredOrgReceipts decorates organization.ReceiptResolver (port "org_receipts").
type measuredOrgReceipts struct {
	inner organization.ReceiptResolver
	reg   *observability.Registry
}

func (m *measuredOrgReceipts) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*organization.OrganizationCommandReceipt, error) {
	start := time.Now()
	r, err := m.inner.ResolveCommand(ctx, operationID, commandName, expectedFingerprint)
	measure(m.reg, "org_receipts", start, err)
	return r, err
}

// measuredInbox decorates organization.InboxConsumer (port "inbox").
type measuredInbox struct {
	inner organization.InboxConsumer
	reg   *observability.Registry
}

func (m *measuredInbox) HandleIAMUserDeletedV1(ctx context.Context, event organization.IAMUserDeletedEvent) error {
	start := time.Now()
	err := m.inner.HandleIAMUserDeletedV1(ctx, event)
	measure(m.reg, "inbox", start, err)
	return err
}

// Compile-time guards: every decorated type satisfies its port.
var (
	_ iam.AuthService                = (*measuredAuth)(nil)
	_ iam.IdentityService            = (*measuredIdentity)(nil)
	_ iam.RoleService                = (*measuredRoles)(nil)
	_ iam.ManagedUserService         = (*measuredManaged)(nil)
	_ iam.ReceiptResolver            = (*measuredIAMReceipts)(nil)
	_ organization.DepartmentService = (*measuredDepartments)(nil)
	_ organization.MembershipService = (*measuredMembership)(nil)
	_ organization.ReceiptResolver   = (*measuredOrgReceipts)(nil)
	_ organization.InboxConsumer     = (*measuredInbox)(nil)
)
