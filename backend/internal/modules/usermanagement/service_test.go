package usermanagement

import (
	"context"
	"testing"
	"time"

	departmentdirectory "vue-element-plus-admin/backend/internal/directory/department"
	casbinrbac "vue-element-plus-admin/backend/internal/middleware/casbin"
	"vue-element-plus-admin/backend/internal/security"
	"vue-element-plus-admin/backend/pb"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"google.golang.org/grpc"
)

const serviceTestModel = `[request_definition]
r = sub, obj
[policy_definition]
p = sub, obj
[role_definition]
g = _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj`

func serviceTestEnforcer(t *testing.T) *casbin.SyncedEnforcer {
	t.Helper()
	accessModel, err := model.NewModelFromString(serviceTestModel)
	if err != nil {
		t.Fatal(err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(accessModel)
	if err != nil {
		t.Fatal(err)
	}
	return enforcer
}

type repositoryStub struct {
	createdInput Input
	passwordHash string
	updated      bool
	listed       []UserItem
}

func (s *repositoryStub) List(context.Context, Filter) ([]UserItem, int, error) {
	return s.listed, len(s.listed), nil
}
func (s *repositoryStub) Create(_ context.Context, input Input, passwordHash string) error {
	s.createdInput = input
	s.passwordHash = passwordHash
	return nil
}
func (s *repositoryStub) Update(context.Context, int64, Input, string) (bool, error) {
	return s.updated, nil
}
func (s *repositoryStub) Delete(context.Context, []int64) error { return nil }
func (s *repositoryStub) CountByDepartment(context.Context, int64) (int64, error) {
	return int64(len(s.listed)), nil
}
func (s *repositoryStub) GetUserIDByUsername(context.Context, string) (int64, error) {
	return 42, nil
}
func (s *repositoryStub) GetRoleCode(context.Context, int64) (string, error) {
	return "test", nil
}
func (s *repositoryStub) GetRoleIDsByCodes(_ context.Context, roleCodes []string) ([]int64, error) {
	roleIDs := make([]int64, len(roleCodes))
	for index := range roleCodes {
		roleIDs[index] = int64(index + 1)
	}
	return roleIDs, nil
}

type departmentDirectoryStub struct {
	requestedIDs []int64
	items        []DepartmentItem
	getItem      DepartmentItem
	getErr       error
}

func (s *departmentDirectoryStub) GetDepartment(
	context.Context,
	*pb.GetDepartmentRequest,
	...grpc.CallOption,
) (*pb.GetDepartmentResponse, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return departmentResponse(s.getItem), nil
}

func (s *departmentDirectoryStub) BatchGetDepartments(
	_ context.Context,
	request *pb.BatchGetDepartmentsRequest,
	_ ...grpc.CallOption,
) (*pb.BatchGetDepartmentsResponse, error) {
	s.requestedIDs = request.Ids
	departments := make([]*pb.GetDepartmentResponse, 0, len(s.items))
	for _, item := range s.items {
		departments = append(departments, departmentResponse(item))
	}
	return &pb.BatchGetDepartmentsResponse{Departments: departments}, nil
}

func departmentResponse(item DepartmentItem) *pb.GetDepartmentResponse {
	return &pb.GetDepartmentResponse{
		Id: item.ID, DepartmentName: item.DepartmentName, Status: item.Status != 0,
		Remark: item.Remark, Deleting: item.Deleting,
	}
}

func testDepartmentDirectory(client *departmentDirectoryStub) *departmentdirectory.Directory {
	return departmentdirectory.New(client, time.Second)
}

func TestServiceCreateNormalizesAndHashes(t *testing.T) {
	repository := &repositoryStub{}
	enforcer := serviceTestEnforcer(t)
	service := NewService(
		repository,
		testDepartmentDirectory(&departmentDirectoryStub{getItem: DepartmentItem{ID: 9, Status: 1}}),
		enforcer,
	)
	input := Input{Username: "alice", Password: "secret123", RoleID: 2}
	input.Department.ID = 9
	if err := service.Create(context.Background(), input); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if repository.createdInput.Account != "alice" || repository.createdInput.DepartmentID != 9 {
		t.Fatalf("normalized input = %#v", repository.createdInput)
	}
	if repository.passwordHash == input.Password || !security.Verify(input.Password, repository.passwordHash) {
		t.Fatal("password was not securely hashed")
	}
	roles, err := enforcer.GetRolesForUser(casbinrbac.UserSubject(42))
	if err != nil || len(roles) != 1 || roles[0] != casbinrbac.RoleSubject("test") {
		t.Fatalf("Casbin user roles = %v, error = %v", roles, err)
	}
}

func TestServiceRejectsDisabledDepartment(t *testing.T) {
	repository := &repositoryStub{}
	directory := testDepartmentDirectory(&departmentDirectoryStub{getItem: DepartmentItem{ID: 9, Status: 0}})
	err := NewService(repository, directory, serviceTestEnforcer(t)).Create(context.Background(), Input{
		Username: "alice", Password: "secret123", RoleID: 2, DepartmentID: 9,
	})
	if err != ErrDepartmentDisabled {
		t.Fatalf("Create() error = %v, want %v", err, ErrDepartmentDisabled)
	}
	if repository.passwordHash != "" {
		t.Fatal("disabled department reached user repository")
	}
}

func TestServiceRejectsDeletingDepartment(t *testing.T) {
	repository := &repositoryStub{}
	directory := testDepartmentDirectory(&departmentDirectoryStub{getItem: DepartmentItem{ID: 9, Status: 1, Deleting: true}})
	err := NewService(repository, directory, serviceTestEnforcer(t)).Create(context.Background(), Input{
		Username: "alice", Password: "secret123", RoleID: 2, DepartmentID: 9,
	})
	if err != ErrDepartmentDeleting {
		t.Fatalf("Create() error = %v, want %v", err, ErrDepartmentDeleting)
	}
	if repository.passwordHash != "" {
		t.Fatal("deleting department reached user repository")
	}
}

func TestServiceUpdateNotFound(t *testing.T) {
	repository := &repositoryStub{}
	err := NewService(repository, nil, serviceTestEnforcer(t)).Update(context.Background(), 3, Input{
		Username: "alice",
		RoleID:   2,
	})
	if err != ErrUserNotFound {
		t.Fatalf("Update() error = %v, want %v", err, ErrUserNotFound)
	}
}

func TestNormalizeInputKeepsPrimaryRoleConsistentWithRoleIDs(t *testing.T) {
	input := Input{RoleID: 2, RoleIDs: []int64{1}}

	normalizeInput(&input)

	if input.RoleID != 1 {
		t.Fatalf("RoleID = %d, want first selected role ID 1", input.RoleID)
	}
}

func TestServiceEnrichesDepartmentsWithoutDatabaseJoin(t *testing.T) {
	repository := &repositoryStub{listed: []UserItem{
		{ID: 1, DepartmentID: 9},
		{ID: 2, DepartmentID: 9},
		{ID: 3},
	}}
	directoryClient := &departmentDirectoryStub{items: []DepartmentItem{{ID: 9, DepartmentName: "研发部"}}}
	items, total, err := NewService(repository, testDepartmentDirectory(directoryClient), serviceTestEnforcer(t)).List(
		context.Background(), Filter{Page: 1, Size: 10},
	)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 3 || len(directoryClient.requestedIDs) != 1 || directoryClient.requestedIDs[0] != 9 {
		t.Fatalf("total = %d, requested IDs = %v", total, directoryClient.requestedIDs)
	}
	if items[0].Department == nil || items[0].Department.DepartmentName != "研发部" {
		t.Fatalf("first department = %#v", items[0].Department)
	}
	if items[1].Department == nil || items[1].Department.ID != 9 {
		t.Fatalf("second department = %#v", items[1].Department)
	}
	if items[2].Department != nil {
		t.Fatalf("third department = %#v, want nil", items[2].Department)
	}
}
