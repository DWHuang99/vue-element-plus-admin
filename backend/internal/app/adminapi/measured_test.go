package adminapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

// fakeIAM satisfies every IAM port with a canned error (nil = success) and
// zero-value results.
type fakeIAM struct {
	err error
}

func (f *fakeIAM) Register(ctx context.Context, username, password string, requestCtx iam.OperationContext) (iam.AuthSession, error) {
	return iam.AuthSession{}, f.err
}
func (f *fakeIAM) Login(ctx context.Context, username, password string, requestCtx iam.OperationContext) (iam.AuthSession, error) {
	return iam.AuthSession{}, f.err
}
func (f *fakeIAM) RevokeSession(ctx context.Context, rawToken string, requestCtx iam.OperationContext) error {
	return f.err
}
func (f *fakeIAM) Authenticate(ctx context.Context, rawToken string, requestCtx iam.OperationContext) (iam.Principal, error) {
	return iam.Principal{}, f.err
}
func (f *fakeIAM) GetIdentity(ctx context.Context, userID int64) (iam.IdentityProfile, error) {
	return iam.IdentityProfile{}, f.err
}
func (f *fakeIAM) GetAuthorizationProfile(ctx context.Context, userID int64) (iam.AuthorizationProfile, error) {
	return iam.AuthorizationProfile{}, f.err
}
func (f *fakeIAM) HasPermission(ctx context.Context, userID int64, permissionCode string) (bool, error) {
	return false, f.err
}
func (f *fakeIAM) BatchGetManagedIdentities(ctx context.Context, candidateUserIDs []int64,
	usernameFilter, accountFilter *string, pageIndex, pageSize int) (iam.ManagedIdentityPage, error) {
	return iam.ManagedIdentityPage{}, f.err
}
func (f *fakeIAM) ListRoles(ctx context.Context) ([]iam.RoleSummary, error) { return nil, f.err }
func (f *fakeIAM) ValidateRoleIDs(ctx context.Context, ids []int64) ([]iam.RoleSummary, error) {
	return nil, f.err
}
func (f *fakeIAM) SaveRole(ctx context.Context, requestCtx iam.OperationContext, id *int64, name, code string) (int64, error) {
	return 0, f.err
}
func (f *fakeIAM) DeleteRoles(ctx context.Context, requestCtx iam.OperationContext, ids []int64) error {
	return f.err
}
func (f *fakeIAM) CreateProvisioningUser(ctx context.Context, requestCtx iam.OperationContext,
	username string, account, email *string, password string, roleIDs []int64) (iam.CreateUserResult, error) {
	return iam.CreateUserResult{}, f.err
}
func (f *fakeIAM) ActivateUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64) (int64, error) {
	return 0, f.err
}
func (f *fakeIAM) DisableUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64, reasonCode string) (int64, error) {
	return 0, f.err
}
func (f *fakeIAM) UpdateManagedUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64,
	username string, account, email *string, password *string, roleIDs []int64) (int64, error) {
	return 0, f.err
}
func (f *fakeIAM) DeleteUsers(ctx context.Context, requestCtx iam.OperationContext, targets []iam.DeleteTarget) ([]iam.BatchDeleteResultItem, error) {
	return nil, f.err
}
func (f *fakeIAM) CompensateProvisioningUser(ctx context.Context, requestCtx iam.OperationContext, userID, expectedVersion int64) error {
	return f.err
}
func (f *fakeIAM) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*iam.IAMCommandReceipt, error) {
	return nil, f.err
}

// fakeOrg satisfies every Organization port with a canned error.
type fakeOrg struct {
	err error
}

func (f *fakeOrg) ListDepartmentTree(ctx context.Context) ([]organization.DepartmentNode, error) {
	return nil, f.err
}
func (f *fakeOrg) GetDepartment(ctx context.Context, departmentID int64) (organization.Department, error) {
	return organization.Department{}, f.err
}
func (f *fakeOrg) ValidateDepartment(ctx context.Context, departmentID *int64) (*organization.Department, error) {
	return nil, f.err
}
func (f *fakeOrg) SaveDepartment(ctx context.Context, requestCtx organization.OperationContext, id *int64, name string, parentID *int64) (int64, error) {
	return 0, f.err
}
func (f *fakeOrg) DeleteDepartments(ctx context.Context, requestCtx organization.OperationContext, ids []int64) error {
	return f.err
}
func (f *fakeOrg) GetUserDepartment(ctx context.Context, userID int64) (*organization.MembershipState, error) {
	return nil, f.err
}
func (f *fakeOrg) BatchGetUserDepartments(ctx context.Context, userIDs []int64) (map[int64]organization.MembershipState, error) {
	return nil, f.err
}
func (f *fakeOrg) ListUserIDsByDepartment(ctx context.Context, departmentID int64) ([]int64, error) {
	return nil, f.err
}
func (f *fakeOrg) SetUserDepartment(ctx context.Context, requestCtx organization.OperationContext,
	userID, departmentID int64, expectedMembershipVersion *int64) (organization.MembershipMutationResult, error) {
	return organization.MembershipMutationResult{}, f.err
}
func (f *fakeOrg) ClearUserDepartment(ctx context.Context, requestCtx organization.OperationContext,
	userID int64, expectedMembershipVersion *int64) (organization.MembershipMutationResult, error) {
	return organization.MembershipMutationResult{}, f.err
}
func (f *fakeOrg) RestoreUserDepartment(ctx context.Context, requestCtx organization.OperationContext,
	userID int64, expectedCurrentMembershipVersion int64,
	previousDepartmentID *int64, previousMembershipVersion *int64) (organization.MembershipMutationResult, error) {
	return organization.MembershipMutationResult{}, f.err
}
func (f *fakeOrg) ClearMembershipsForUsers(ctx context.Context, requestCtx organization.OperationContext, userIDs []int64) error {
	return f.err
}
func (f *fakeOrg) HandleIAMUserDeletedV1(ctx context.Context, event organization.IAMUserDeletedEvent) error {
	return f.err
}
func (f *fakeOrg) ResolveCommand(ctx context.Context, operationID, commandName, expectedFingerprint string) (*organization.OrganizationCommandReceipt, error) {
	return nil, f.err
}

// iamDecorators builds the five IAM decorators around one fake.
func iamDecorators(f *fakeIAM, reg *observability.Registry) []func() error {
	calls := []func() error{
		func() error {
			_, err := (&measuredAuth{inner: f, reg: reg}).Register(context.Background(), "u", "p", iam.OperationContext{})
			return err
		},
		func() error {
			_, err := (&measuredAuth{inner: f, reg: reg}).Login(context.Background(), "u", "p", iam.OperationContext{})
			return err
		},
		func() error {
			return (&measuredAuth{inner: f, reg: reg}).RevokeSession(context.Background(), "t", iam.OperationContext{})
		},
		func() error {
			_, err := (&measuredAuth{inner: f, reg: reg}).Authenticate(context.Background(), "t", iam.OperationContext{})
			return err
		},
		func() error {
			_, err := (&measuredIdentity{inner: f, reg: reg}).GetIdentity(context.Background(), 1)
			return err
		},
		func() error {
			_, err := (&measuredIdentity{inner: f, reg: reg}).GetAuthorizationProfile(context.Background(), 1)
			return err
		},
		func() error {
			_, err := (&measuredIdentity{inner: f, reg: reg}).HasPermission(context.Background(), 1, "x")
			return err
		},
		func() error {
			_, err := (&measuredIdentity{inner: f, reg: reg}).BatchGetManagedIdentities(context.Background(), nil, nil, nil, 1, 20)
			return err
		},
		func() error {
			_, err := (&measuredRoles{inner: f, reg: reg}).ListRoles(context.Background())
			return err
		},
		func() error {
			_, err := (&measuredRoles{inner: f, reg: reg}).ValidateRoleIDs(context.Background(), []int64{1})
			return err
		},
		func() error {
			_, err := (&measuredRoles{inner: f, reg: reg}).SaveRole(context.Background(), iam.OperationContext{}, nil, "n", "c")
			return err
		},
		func() error {
			return (&measuredRoles{inner: f, reg: reg}).DeleteRoles(context.Background(), iam.OperationContext{}, []int64{1})
		},
		func() error {
			_, err := (&measuredManaged{inner: f, reg: reg}).CreateProvisioningUser(context.Background(), iam.OperationContext{}, "u", nil, nil, "p", nil)
			return err
		},
		func() error {
			_, err := (&measuredManaged{inner: f, reg: reg}).ActivateUser(context.Background(), iam.OperationContext{}, 1, 1)
			return err
		},
		func() error {
			_, err := (&measuredManaged{inner: f, reg: reg}).DisableUser(context.Background(), iam.OperationContext{}, 1, 1, "r")
			return err
		},
		func() error {
			_, err := (&measuredManaged{inner: f, reg: reg}).UpdateManagedUser(context.Background(), iam.OperationContext{}, 1, 1, "u", nil, nil, nil, nil)
			return err
		},
		func() error {
			_, err := (&measuredManaged{inner: f, reg: reg}).DeleteUsers(context.Background(), iam.OperationContext{}, nil)
			return err
		},
		func() error {
			return (&measuredManaged{inner: f, reg: reg}).CompensateProvisioningUser(context.Background(), iam.OperationContext{}, 1, 1)
		},
		func() error {
			_, err := (&measuredIAMReceipts{inner: f, reg: reg}).ResolveCommand(context.Background(), "op", "cmd", "fp")
			return err
		},
	}
	return calls
}

// orgDecorators builds the four Organization decorators around one fake.
func orgDecorators(f *fakeOrg, reg *observability.Registry) []func() error {
	calls := []func() error{
		func() error {
			_, err := (&measuredDepartments{inner: f, reg: reg}).ListDepartmentTree(context.Background())
			return err
		},
		func() error {
			_, err := (&measuredDepartments{inner: f, reg: reg}).GetDepartment(context.Background(), 1)
			return err
		},
		func() error {
			_, err := (&measuredDepartments{inner: f, reg: reg}).ValidateDepartment(context.Background(), nil)
			return err
		},
		func() error {
			_, err := (&measuredDepartments{inner: f, reg: reg}).SaveDepartment(context.Background(), organization.OperationContext{}, nil, "n", nil)
			return err
		},
		func() error {
			return (&measuredDepartments{inner: f, reg: reg}).DeleteDepartments(context.Background(), organization.OperationContext{}, []int64{1})
		},
		func() error {
			_, err := (&measuredMembership{inner: f, reg: reg}).GetUserDepartment(context.Background(), 1)
			return err
		},
		func() error {
			_, err := (&measuredMembership{inner: f, reg: reg}).BatchGetUserDepartments(context.Background(), []int64{1})
			return err
		},
		func() error {
			_, err := (&measuredMembership{inner: f, reg: reg}).ListUserIDsByDepartment(context.Background(), 1)
			return err
		},
		func() error {
			_, err := (&measuredMembership{inner: f, reg: reg}).SetUserDepartment(context.Background(), organization.OperationContext{}, 1, 1, nil)
			return err
		},
		func() error {
			_, err := (&measuredMembership{inner: f, reg: reg}).ClearUserDepartment(context.Background(), organization.OperationContext{}, 1, nil)
			return err
		},
		func() error {
			_, err := (&measuredMembership{inner: f, reg: reg}).RestoreUserDepartment(context.Background(), organization.OperationContext{}, 1, 1, nil, nil)
			return err
		},
		func() error {
			return (&measuredMembership{inner: f, reg: reg}).ClearMembershipsForUsers(context.Background(), organization.OperationContext{}, []int64{1})
		},
		func() error {
			return (&measuredInbox{inner: f, reg: reg}).HandleIAMUserDeletedV1(context.Background(), organization.IAMUserDeletedEvent{})
		},
		func() error {
			_, err := (&measuredOrgReceipts{inner: f, reg: reg}).ResolveCommand(context.Background(), "op", "cmd", "fp")
			return err
		},
	}
	return calls
}

func TestMeasuredPorts_RecordCallsAndLatency(t *testing.T) {
	reg := observability.NewRegistry()
	iamCalls := iamDecorators(&fakeIAM{}, reg)
	orgCalls := orgDecorators(&fakeOrg{}, reg)

	for _, call := range append(iamCalls, orgCalls...) {
		require.NoError(t, call())
	}

	// One call per interface method; method counts differ per port.
	expected := map[string]int64{
		"auth": 4, "identity": 4, "roles": 4, "managed_users": 6, "iam_receipts": 1,
		"departments": 5, "membership": 7, "org_receipts": 1, "inbox": 1,
	}
	snap := map[string]float64{}
	for _, m := range reg.Snapshot() {
		snap[m.Name] = m.Value
	}
	for port, n := range expected {
		assert.Equal(t, n, reg.CounterValue("port_"+port+"_calls_total"), "calls_total for %s", port)
		assert.Equal(t, int64(0), reg.CounterValue("port_"+port+"_errors_total"), "no errors for %s", port)
		assert.Equal(t, float64(n), snap["port_"+port+"_latency_ms_count"], "latency count for %s", port)
		// No-op fakes can complete in sub-microseconds, so the sum is >= 0.
		assert.GreaterOrEqual(t, snap["port_"+port+"_latency_ms_sum_ms"], float64(0), "latency sum for %s", port)
	}
}

func TestMeasuredPorts_ClassifyIAMErrors(t *testing.T) {
	reg := observability.NewRegistry()
	calls := iamDecorators(&fakeIAM{err: iam.ErrInvalidToken}, reg)
	for _, call := range calls {
		require.Error(t, call())
	}
	// Call counts per port: auth 4, identity 4, roles 4, managed 6, receipts 1.
	expected := map[string]int64{
		"auth": 4, "identity": 4, "roles": 4, "managed_users": 6, "iam_receipts": 1,
	}
	var total int64
	for port, n := range expected {
		total += n
		assert.Equal(t, n, reg.CounterValue("port_"+port+"_errors_total"))
		assert.Equal(t, n, reg.CounterValue("port_"+port+"_errors_invalid_token_total"),
			"typed class for %s", port)
	}
	assert.Equal(t, total, reg.CounterValue("port_auth_errors_total")+reg.CounterValue("port_identity_errors_total")+
		reg.CounterValue("port_roles_errors_total")+reg.CounterValue("port_managed_users_errors_total")+
		reg.CounterValue("port_iam_receipts_errors_total"))
}

func TestMeasuredPorts_ClassifyOrgErrors(t *testing.T) {
	reg := observability.NewRegistry()
	calls := orgDecorators(&fakeOrg{err: organization.ErrDepartmentNotFound}, reg)
	for _, call := range calls {
		require.Error(t, call())
	}
	assert.Equal(t, int64(5), reg.CounterValue("port_departments_errors_total"))
	assert.Equal(t, int64(5), reg.CounterValue("port_departments_errors_department_not_found_total"))
	assert.Equal(t, int64(7), reg.CounterValue("port_membership_errors_total"))
	assert.Equal(t, int64(7), reg.CounterValue("port_membership_errors_department_not_found_total"))
	assert.Equal(t, int64(1), reg.CounterValue("port_inbox_errors_total"))
	assert.Equal(t, int64(1), reg.CounterValue("port_org_receipts_errors_total"))
}

func TestMeasuredPorts_WrappedDomainErrorStillClassified(t *testing.T) {
	reg := observability.NewRegistry()
	inner := &fakeIAM{err: fmt.Errorf("delivery failed: %w", iam.ErrUserNotFound)}
	_, err := (&measuredIdentity{inner: inner, reg: reg}).GetIdentity(context.Background(), 1)
	require.Error(t, err)

	assert.Equal(t, int64(1), reg.CounterValue("port_identity_errors_total"))
	assert.Equal(t, int64(1), reg.CounterValue("port_identity_errors_user_not_found_total"))
}

func TestMeasuredPorts_UnknownErrorIsInternal(t *testing.T) {
	reg := observability.NewRegistry()
	inner := &fakeIAM{err: errors.New("boom")}
	_, err := (&measuredIdentity{inner: inner, reg: reg}).GetIdentity(context.Background(), 1)
	require.Error(t, err)

	assert.Equal(t, int64(1), reg.CounterValue("port_identity_errors_total"))
	assert.Equal(t, int64(1), reg.CounterValue("port_identity_errors_internal_total"))
}

func TestMeasure_NilRegistryIsPassThrough(t *testing.T) {
	// measure must not panic when the registry is nil (routers without
	// metrics enabled share the same decorators).
	inner := &fakeIAM{err: iam.ErrUserNotFound}
	_, err := (&measuredIdentity{inner: inner, reg: nil}).GetIdentity(context.Background(), 1)
	assert.ErrorIs(t, err, iam.ErrUserNotFound)
}
