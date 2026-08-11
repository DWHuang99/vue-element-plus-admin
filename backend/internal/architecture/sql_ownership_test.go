// SQL ownership tests (quickstart Checkpoint B, T016).
//
// Each owner query package may reference exactly its own tables
// (data-model.md Ownership matrix). The only cross-representation SQL is the
// reviewed temporary Platform bridge inside db/migrations — the migrator
// installs it and it is not generated into any module's sqlc package — so
// migrations are deliberately NOT scanned here.
package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type sqlOwnershipRule struct {
	dir       string
	forbidden []string // table names that must not appear in these queries
}

// Table names are matched as whole words, so the legacy users.department_id
// column (documented compatibility representation, IAM-owned) never trips the
// departments rule, and "user_roles" never trips "users".
var sqlOwnershipRules = []sqlOwnershipRule{
	{
		dir: "db/iam/queries",
		forbidden: []string{
			"departments", "organization_user_departments",
			"organization_command_receipts", "organization_inbox_messages",
			"admin_workflows", "admin_workflow_subjects", "admin_workflow_recovery_actions",
			"compatibility_bridge_mode", "compatibility_bridge_mode_changes", "compatibility_rollout_gates",
		},
	},
	{
		dir: "db/organization/queries",
		forbidden: []string{
			"users", "sessions", "roles", "user_roles", "permissions", "role_permissions",
			"iam_command_receipts", "iam_outbox_events", "iam_outbox_requeues",
			"admin_workflows", "admin_workflow_subjects", "admin_workflow_recovery_actions",
			"compatibility_bridge_mode", "compatibility_bridge_mode_changes", "compatibility_rollout_gates",
		},
	},
	{
		dir: "db/adminbff/queries",
		forbidden: []string{
			"users", "sessions", "roles", "user_roles", "permissions", "role_permissions",
			"departments", "organization_user_departments",
			"iam_command_receipts", "iam_outbox_events", "iam_outbox_requeues",
			"organization_command_receipts", "organization_inbox_messages",
			"compatibility_bridge_mode", "compatibility_bridge_mode_changes", "compatibility_rollout_gates",
		},
	},
}

func TestSQLQueryOwnership(t *testing.T) {
	scanned := 0
	for _, rule := range sqlOwnershipRules {
		dir := filepath.Join(repoRoot, rule.dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", rule.dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
				continue
			}
			scanned++
			content, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", filepath.Join(rule.dir, e.Name()), err)
			}
			// Comments are documentation, not references: strip -- line and
			// /* */ block comments before scanning.
			sql := string(content)
			sql = regexp.MustCompile(`(?m)--[^\n]*`).ReplaceAllString(sql, "")
			sql = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(sql, "")
			for _, name := range rule.forbidden {
				re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
				if re.MatchString(sql) {
					t.Errorf("%s/%s references %q — %s is not owned by this module",
						rule.dir, e.Name(), name, name)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no query files scanned; check repo layout")
	}
}
