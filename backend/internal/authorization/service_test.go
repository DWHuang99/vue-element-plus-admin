//go:build rollback

package authorization

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
)

type fakePermissionQueries struct {
	permissions []string
	listErr     error
	hasResults  []bool
	hasErr      error
	hasCalls    int
	lastParams  sqlc.HasPermissionByUserIDParams
}

func (f *fakePermissionQueries) ListEffectivePermissionsByUserID(context.Context, int64) ([]string, error) {
	return f.permissions, f.listErr
}

func (f *fakePermissionQueries) HasPermissionByUserID(_ context.Context, arg sqlc.HasPermissionByUserIDParams) (bool, error) {
	f.lastParams = arg
	call := f.hasCalls
	f.hasCalls++
	if f.hasErr != nil {
		return false, f.hasErr
	}
	if call >= len(f.hasResults) {
		return false, nil
	}
	return f.hasResults[call], nil
}

func TestEffectivePermissionsReturnsNonNilEmptySlice(t *testing.T) {
	svc := &AuthorizationService{q: &fakePermissionQueries{permissions: nil}}

	permissions, err := svc.EffectivePermissions(context.Background(), 7)

	require.NoError(t, err)
	require.NotNil(t, permissions)
	assert.Empty(t, permissions)
}

func TestEffectivePermissionsReturnsCurrentGrantSet(t *testing.T) {
	expected := []string{DepartmentsRead, UsersRead}
	svc := &AuthorizationService{q: &fakePermissionQueries{permissions: expected}}

	permissions, err := svc.EffectivePermissions(context.Background(), 7)

	require.NoError(t, err)
	assert.Equal(t, expected, permissions)
}

func TestEffectivePermissionsWrapsQueryError(t *testing.T) {
	svc := &AuthorizationService{q: &fakePermissionQueries{listErr: errors.New("database unavailable")}}

	permissions, err := svc.EffectivePermissions(context.Background(), 7)

	assert.Nil(t, permissions)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list effective permissions")
}

func TestHasPermissionQueriesEveryCall(t *testing.T) {
	queries := &fakePermissionQueries{hasResults: []bool{false, true}}
	svc := &AuthorizationService{q: queries}

	allowed, err := svc.HasPermission(context.Background(), 9, RolesWrite)
	require.NoError(t, err)
	assert.False(t, allowed)

	allowed, err = svc.HasPermission(context.Background(), 9, RolesWrite)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, 2, queries.hasCalls, "authorization decisions must not be cached")
	assert.Equal(t, sqlc.HasPermissionByUserIDParams{UserID: 9, Code: RolesWrite}, queries.lastParams)
}

func TestHasPermissionWrapsQueryError(t *testing.T) {
	svc := &AuthorizationService{q: &fakePermissionQueries{hasErr: errors.New("database unavailable")}}

	allowed, err := svc.HasPermission(context.Background(), 9, UsersRead)

	assert.False(t, allowed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "check permission")
}
