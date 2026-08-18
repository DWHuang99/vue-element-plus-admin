package casbinrbac

import (
	"database/sql"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	userPrefix = "user:"
	rolePrefix = "role:"
)

//go:embed model.conf
var modelText string

// NewEnforcer wires Casbin to the existing IAM PostgreSQL connection. Casbin's
// auto-save remains enabled, so policy mutations are persisted to casbin_rule.
func NewEnforcer(database *sql.DB) (*casbin.SyncedEnforcer, error) {
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open Casbin database connection: %w", err)
	}
	gormadapter.TurnOffAutoMigrate(gormDB)
	adapter, err := gormadapter.NewAdapterByDB(gormDB)
	if err != nil {
		return nil, fmt.Errorf("create Casbin adapter: %w", err)
	}

	accessModel, err := model.NewModelFromString(modelText)
	if err != nil {
		return nil, fmt.Errorf("load embedded Casbin model: %w", err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(accessModel, adapter)
	if err != nil {
		return nil, fmt.Errorf("create Casbin enforcer: %w", err)
	}
	enforcer.EnableAutoSave(true)
	return enforcer, nil
}

func UserSubject(userID int64) string {
	return userPrefix + strconv.FormatInt(userID, 10)
}

func RoleSubject(roleCode string) string {
	return rolePrefix + strings.TrimSpace(roleCode)
}

func RoleCodes(subjects []string) []string {
	roles := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if strings.HasPrefix(subject, rolePrefix) {
			roles = append(roles, strings.TrimPrefix(subject, rolePrefix))
		}
	}
	sort.Strings(roles)
	return roles
}

func PermissionCodes(rules [][]string) []string {
	set := make(map[string]struct{})
	for _, rule := range rules {
		if len(rule) >= 2 && strings.TrimSpace(rule[1]) != "" {
			set[rule[1]] = struct{}{}
		}
	}
	permissions := make([]string, 0, len(set))
	for permissionCode := range set {
		permissions = append(permissions, permissionCode)
	}
	sort.Strings(permissions)
	return permissions
}
