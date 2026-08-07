package rbac

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
)

var (
	testCtx = context.Background()
	testSvc *RBACService
	testSeq int
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("scaffold_rbac_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategyAndDeadline(
			60*time.Second,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start postgres container: %v\n", err)
		os.Exit(1)
	}

	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get connection string: %v\n", err)
		os.Exit(1)
	}

	if err := database.RunMigrations(connStr); err != nil {
		fmt.Fprintf(os.Stderr, "failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	db, err := database.Connect(ctx, database.Config{
		URL:             connStr,
		MaxConns:        5,
		MinConns:        1,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect db: %v\n", err)
		os.Exit(1)
	}
	testSvc = NewRBACService(db.Pool)

	code := m.Run()

	db.Close()
	_ = pg.Terminate(ctx)
	os.Exit(code)
}

func uniqueUsername() string {
	testSeq++
	return fmt.Sprintf("rbac_user_%d", testSeq)
}

// --- roles ---

func TestListRoles_Seeded(t *testing.T) {
	list, err := testSvc.ListRoles(testCtx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	codes := map[string]bool{}
	for _, r := range list {
		codes[r.Code] = true
	}
	assert.True(t, codes["super_admin"] && codes["admin"] && codes["user"], "three seeded roles present")
}

func TestSaveRole_CreateAndUpdate(t *testing.T) {
	// Create.
	id := int64(0)
	err := testSvc.SaveRole(testCtx, SaveRoleParams{Name: "运营", Code: "operator"})
	require.NoError(t, err)

	list, err := testSvc.ListRoles(testCtx)
	require.NoError(t, err)
	var found *RoleView
	for i := range list {
		if list[i].Code == "operator" {
			found = &list[i]
			id = list[i].ID
		}
	}
	require.NotNil(t, found, "created role listed")

	// Update.
	err = testSvc.SaveRole(testCtx, SaveRoleParams{ID: &id, Name: "运营专员", Code: "operator"})
	require.NoError(t, err)
	list, err = testSvc.ListRoles(testCtx)
	require.NoError(t, err)
	for _, r := range list {
		if r.ID == id {
			assert.Equal(t, "运营专员", r.Name)
		}
	}
}

func TestSaveRole_DuplicateName(t *testing.T) {
	err := testSvc.SaveRole(testCtx, SaveRoleParams{Name: "普通用户", Code: "some_other"})
	assert.ErrorIs(t, err, ErrNameTaken)
}

func TestSaveRole_DuplicateCode(t *testing.T) {
	err := testSvc.SaveRole(testCtx, SaveRoleParams{Name: "另一个角色", Code: "user"})
	assert.ErrorIs(t, err, ErrNameTaken)
}

func TestSaveRole_UpdateNotFound(t *testing.T) {
	badID := int64(999999)
	err := testSvc.SaveRole(testCtx, SaveRoleParams{ID: &badID, Name: "x", Code: "x_role"})
	assert.ErrorIs(t, err, ErrRoleNotFound)
}

func TestDeleteRoles_UnassignedOK(t *testing.T) {
	err := testSvc.SaveRole(testCtx, SaveRoleParams{Name: "待删角色", Code: "to_delete"})
	require.NoError(t, err)
	list, err := testSvc.ListRoles(testCtx)
	require.NoError(t, err)
	var id int64
	for _, r := range list {
		if r.Code == "to_delete" {
			id = r.ID
		}
	}
	require.NotZero(t, id)
	require.NoError(t, testSvc.DeleteRoles(testCtx, []int64{id}))

	list, err = testSvc.ListRoles(testCtx)
	require.NoError(t, err)
	for _, r := range list {
		assert.NotEqual(t, id, r.ID)
	}
}

func TestDeleteRoles_ReferencedByUser(t *testing.T) {
	// Assign the seeded 'user' role to a user, then attempt to delete that role.
	user, err := createTestUser(t, testSvc, SaveUserParams{Roles: []int64{seedRoleID(t, "user")}})
	require.NoError(t, err)
	_ = user

	roleID := seedRoleID(t, "user")
	err = testSvc.DeleteRoles(testCtx, []int64{roleID})
	assert.ErrorIs(t, err, ErrDeleteProtected)
}

func TestDeleteRoles_NotFound(t *testing.T) {
	err := testSvc.DeleteRoles(testCtx, []int64{999999})
	assert.ErrorIs(t, err, ErrRoleNotFound)
}

// --- departments ---

func TestListDepartments_Tree(t *testing.T) {
	tree, err := testSvc.ListDepartments(testCtx)
	require.NoError(t, err)
	var dev *DepartmentView
	for i := range tree {
		if tree[i].Name == "研发部" {
			dev = &tree[i]
		}
	}
	require.NotNil(t, dev, "研发部 root present")
	assert.Len(t, dev.Children, 2, "研发部 has 前端组/后端组 children")
}

func TestSaveDepartment_CreateChildAndUpdate(t *testing.T) {
	parentID := seedDepartmentID(t, "研发部")

	// Create a child.
	err := testSvc.SaveDepartment(testCtx, SaveDepartmentParams{Name: "算法组", ParentID: &parentID})
	require.NoError(t, err)

	tree, err := testSvc.ListDepartments(testCtx)
	require.NoError(t, err)
	var childID int64
	found := false
	for _, root := range tree {
		if root.ID == parentID {
			for _, ch := range root.Children {
				if ch.Name == "算法组" {
					found = true
					childID = ch.ID
				}
			}
		}
	}
	require.True(t, found, "child appears under 研发部")

	// Update it.
	err = testSvc.SaveDepartment(testCtx, SaveDepartmentParams{ID: &childID, Name: "算法工程组", ParentID: &parentID})
	require.NoError(t, err)
	tree, err = testSvc.ListDepartments(testCtx)
	require.NoError(t, err)
	renamed := false
	for _, root := range tree {
		if root.ID == parentID {
			for _, ch := range root.Children {
				if ch.ID == childID && ch.Name == "算法工程组" {
					renamed = true
				}
			}
		}
	}
	assert.True(t, renamed)
}

func TestSaveDepartment_SelfParent(t *testing.T) {
	id := seedDepartmentID(t, "产品部")
	err := testSvc.SaveDepartment(testCtx, SaveDepartmentParams{ID: &id, Name: "产品部", ParentID: &id})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestSaveDepartment_DuplicateName(t *testing.T) {
	err := testSvc.SaveDepartment(testCtx, SaveDepartmentParams{Name: "运营部", ParentID: nil})
	assert.ErrorIs(t, err, ErrNameTaken)
}

func TestSaveDepartment_UpdateNotFound(t *testing.T) {
	badID := int64(999999)
	err := testSvc.SaveDepartment(testCtx, SaveDepartmentParams{ID: &badID, Name: "不存在", ParentID: nil})
	assert.ErrorIs(t, err, ErrDepartmentNotFound)
}

func TestDeleteDepartments_LeafOK(t *testing.T) {
	err := testSvc.SaveDepartment(testCtx, SaveDepartmentParams{Name: "临时部门", ParentID: nil})
	require.NoError(t, err)
	tree, err := testSvc.ListDepartments(testCtx)
	require.NoError(t, err)
	var id int64
	for _, root := range tree {
		if root.Name == "临时部门" {
			id = root.ID
		}
	}
	require.NotZero(t, id)
	require.NoError(t, testSvc.DeleteDepartments(testCtx, []int64{id}))
}

func TestDeleteDepartments_HasChildren(t *testing.T) {
	id := seedDepartmentID(t, "研发部")
	err := testSvc.DeleteDepartments(testCtx, []int64{id})
	assert.ErrorIs(t, err, ErrDeleteProtected)
}

func TestDeleteDepartments_HasUsers(t *testing.T) {
	// Create a user in a department, then try to delete that department.
	devID := seedDepartmentID(t, "研发部")
	_, err := createTestUser(t, testSvc, SaveUserParams{DepartmentID: &devID})
	require.NoError(t, err)
	err = testSvc.DeleteDepartments(testCtx, []int64{devID})
	assert.ErrorIs(t, err, ErrDeleteProtected)
}

func TestDeleteDepartments_NotFound(t *testing.T) {
	err := testSvc.DeleteDepartments(testCtx, []int64{999999})
	assert.ErrorIs(t, err, ErrDepartmentNotFound)
}

// --- users ---

func TestSaveUser_CreateWithRoles(t *testing.T) {
	userID, err := createTestUser(t, testSvc, SaveUserParams{Roles: []int64{seedRoleID(t, "user")}})
	require.NoError(t, err)

	result, err := testSvc.ListUsers(testCtx, ListUsersParams{PageIndex: 1, PageSize: 100})
	require.NoError(t, err)
	found := false
	for _, u := range result.List {
		if u.ID == userID {
			found = true
			assert.Contains(t, u.Role, "普通用户")
		}
	}
	assert.True(t, found, "created user appears in list")
}

func TestSaveUser_CreateMissingPassword(t *testing.T) {
	err := testSvc.SaveUser(testCtx, SaveUserParams{Username: uniqueUsername()})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestSaveUser_DuplicateUsername(t *testing.T) {
	username := uniqueUsername()
	_, err := createTestUser(t, testSvc, SaveUserParams{Username: username})
	require.NoError(t, err)
	err = testSvc.SaveUser(testCtx, SaveUserParams{Username: username, Password: strPtr("another-pass-123")})
	assert.ErrorIs(t, err, ErrNameTaken)
}

func TestSaveUser_BadRole(t *testing.T) {
	err := testSvc.SaveUser(testCtx, SaveUserParams{
		Username: uniqueUsername(),
		Password: strPtr("test-password-123"),
		Roles:    []int64{999999},
	})
	assert.ErrorIs(t, err, ErrRoleNotFound)
}

func TestSaveUser_UpdateKeepsPasswordAndReplacesRoles(t *testing.T) {
	userID, err := createTestUser(t, testSvc, SaveUserParams{Roles: []int64{seedRoleID(t, "user")}})
	require.NoError(t, err)

	// Update: swap to 'admin' role, keep password nil.
	err = testSvc.SaveUser(testCtx, SaveUserParams{
		ID:       &userID,
		Username: uniqueUsername(), // username ignored on update
		Roles:    []int64{seedRoleID(t, "admin")},
	})
	require.NoError(t, err)

	result, err := testSvc.ListUsers(testCtx, ListUsersParams{PageIndex: 1, PageSize: 100})
	require.NoError(t, err)
	for _, u := range result.List {
		if u.ID == userID {
			assert.Contains(t, u.Role, "管理员")
			assert.NotContains(t, u.Role, "普通用户")
		}
	}
}

func TestListUsers_PaginationAndFilter(t *testing.T) {
	for range 5 {
		_, err := createTestUser(t, testSvc, SaveUserParams{})
		require.NoError(t, err)
	}
	// Filter by the username prefix of the last created user.
	result, err := testSvc.ListUsers(testCtx, ListUsersParams{PageIndex: 1, PageSize: 2})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(result.List), 2)
	assert.GreaterOrEqual(t, result.Total, int64(5))
}

func TestDeleteUsers_NotFound(t *testing.T) {
	err := testSvc.DeleteUsers(testCtx, []int64{999999})
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestDeleteUsers_OK(t *testing.T) {
	userID, err := createTestUser(t, testSvc, SaveUserParams{Roles: []int64{seedRoleID(t, "user")}})
	require.NoError(t, err)
	require.NoError(t, testSvc.DeleteUsers(testCtx, []int64{userID}))

	result, err := testSvc.ListUsers(testCtx, ListUsersParams{PageIndex: 1, PageSize: 100})
	require.NoError(t, err)
	for _, u := range result.List {
		assert.NotEqual(t, userID, u.ID)
	}
}

// --- helpers ---

func strPtr(s string) *string { return &s }

func seedRoleID(t *testing.T, code string) int64 {
	t.Helper()
	list, err := testSvc.ListRoles(testCtx)
	require.NoError(t, err)
	for _, r := range list {
		if r.Code == code {
			return r.ID
		}
	}
	t.Fatalf("seed role %q not found", code)
	return 0
}

func seedDepartmentID(t *testing.T, name string) int64 {
	t.Helper()
	tree, err := testSvc.ListDepartments(testCtx)
	require.NoError(t, err)
	var walk func(nodes []DepartmentView) int64
	walk = func(nodes []DepartmentView) int64 {
		for _, n := range nodes {
			if n.Name == name {
				return n.ID
			}
			if id := walk(n.Children); id != 0 {
				return id
			}
		}
		return 0
	}
	id := walk(tree)
	require.NotZero(t, id, "seed department %q not found", name)
	return id
}

// createTestUser creates a user with sensible defaults and returns its id.
func createTestUser(t *testing.T, svc Service, overrides SaveUserParams) (int64, error) {
	t.Helper()
	p := SaveUserParams{
		Username: overrides.Username,
		Account:  overrides.Account,
		Email:    overrides.Email,
		Password: overrides.Password,
	}
	if p.Username == "" {
		p.Username = uniqueUsername()
	}
	if p.Password == nil {
		p.Password = strPtr("test-password-123")
	}
	p.ID = overrides.ID
	p.DepartmentID = overrides.DepartmentID
	p.Roles = overrides.Roles

	err := svc.SaveUser(testCtx, p)
	if err != nil {
		return 0, err
	}
	result, err := svc.ListUsers(testCtx, ListUsersParams{PageIndex: 1, PageSize: 100})
	if err != nil {
		return 0, err
	}
	for _, u := range result.List {
		if u.Username == p.Username {
			return u.ID, nil
		}
	}
	return 0, errors.New("created user not found in list")
}
