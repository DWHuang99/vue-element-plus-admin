// Architecture boundary tests (quickstart Checkpoint B, T009).
//
// Uses Go AST parsing (no go/packages dependency): every non-test Go file
// under internal/ is scanned for imports and checked against the ownership
// rules from plan.md Architecture:
//
//	internal/iam          ↛ internal/organization
//	internal/organization ↛ internal/iam
//	internal/adminbff     ↛ pgx / any generated sqlc package / migrations / internal/database
//
// The only sanctioned exception is the workflow-store adapter
// internal/adminbff/postgres (mirrors the iam/organization postgres
// adapters): it may import pgx and only the BFF-owned workflow sqlc package.
// Test files (_test.go) are exempt because compatibility suites legitimately
// wire real adapters (the compat suite is the baseline arbiter).
package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is backend/ (this package's grandparent).
var repoRoot = filepath.Clean(filepath.Join("..", ".."))

// forbiddenImportsFor checks the plan.md rules for a package path.
func forbiddenImportsFor(pkg string) (rule string, matches func(imp string) bool) {
	switch {
	case pkg == "internal/iam" || strings.HasPrefix(pkg, "internal/iam/"):
		return "iam must not import organization", func(imp string) bool {
			return imp == "github.com/hdw/vue-element-plus-admin/backend/internal/organization" ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/internal/organization/")
		}
	case pkg == "internal/organization" || strings.HasPrefix(pkg, "internal/organization/"):
		return "organization must not import iam", func(imp string) bool {
			return imp == "github.com/hdw/vue-element-plus-admin/backend/internal/iam" ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/internal/iam/")
		}
	case pkg == "internal/adminbff" || strings.HasPrefix(pkg, "internal/adminbff/"):
		// The workflow-store adapter is the only sanctioned exception.
		if pkg == "internal/adminbff/postgres" || strings.HasPrefix(pkg, "internal/adminbff/postgres/") {
			return "adminbff/postgres must not import global sqlc / migrations / database", func(imp string) bool {
				return imp == "github.com/hdw/vue-element-plus-admin/backend/internal/database" ||
					imp == "github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc" ||
					strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/db/migrations")
			}
		}
		return "adminbff application/transport must not import pgx/sqlc/migrations/database", func(imp string) bool {
			return isPgx(imp) ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/internal/database") ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/db/migrations") ||
				strings.Contains(imp, "/postgres/sqlc") || // any generated sqlc package
				imp == "github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
		}
	case pkg == "internal/integration" || strings.HasPrefix(pkg, "internal/integration/"):
		// Integration (outbox dispatcher) touches only owner application
		// ports — never adapters, pgx, sqlc, producer tables, and never HTTP
		// (contracts/domain-events.md: event records are not public HTTP
		// models).
		return "integration must not import adapters/pgx/sqlc/migrations/database/gin", func(imp string) bool {
			return imp == "github.com/gin-gonic/gin" ||
				isPgx(imp) ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/internal/database") ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/db/migrations") ||
				strings.Contains(imp, "/postgres/sqlc") ||
				imp == "github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres" ||
				imp == "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
		}
	case pkg == "internal/platform" || strings.HasPrefix(pkg, "internal/platform/"):
		// Platform operations boundary is HTTP-free (no browser requeue API)
		// and never imports infrastructure or generated code.
		return "platform must not import gin/adapters/pgx/sqlc/migrations/database", func(imp string) bool {
			return imp == "github.com/gin-gonic/gin" ||
				isPgx(imp) ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/internal/database") ||
				strings.HasPrefix(imp, "github.com/hdw/vue-element-plus-admin/backend/db/migrations") ||
				strings.Contains(imp, "/postgres/sqlc") ||
				imp == "github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres" ||
				imp == "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
		}
	}
	return "", nil
}

func isPgx(imp string) bool {
	return imp == "github.com/jackc/pgx" ||
		strings.HasPrefix(imp, "github.com/jackc/pgx/") ||
		strings.HasPrefix(imp, "github.com/jackc/pgx/v")
}

// scanImports returns pkgPath → non-test imports per package under internal/.
func scanImports(t *testing.T) map[string][]string {
	t.Helper()
	internalRoot := filepath.Join(repoRoot, "internal")
	imports := map[string][]string{}

	err := filepath.WalkDir(internalRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		rel, rerr := filepath.Rel(repoRoot, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		pkg := filepath.ToSlash(rel)
		for _, imp := range file.Imports {
			name := strings.Trim(imp.Path.Value, `"`)
			imports[pkg] = append(imports[pkg], name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	return imports
}

func TestIAMOrganizationIsolation(t *testing.T) {
	imports := scanImports(t)
	for pkg, deps := range imports {
		rule, matches := forbiddenImportsFor(pkg)
		if matches == nil {
			continue
		}
		for _, dep := range deps {
			if matches(dep) {
				t.Errorf("%s: %s (imports %s)", pkg, rule, dep)
			}
		}
	}
}

func TestAdminBFFNoInfrastructureImports(t *testing.T) {
	imports := scanImports(t)
	checked := 0
	for pkg, deps := range imports {
		if pkg != "internal/adminbff" && !strings.HasPrefix(pkg, "internal/adminbff/") {
			continue
		}
		checked++
		rule, matches := forbiddenImportsFor(pkg)
		for _, dep := range deps {
			if matches(dep) {
				t.Errorf("%s: %s (imports %s)", pkg, rule, dep)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no adminbff packages scanned; check repo layout")
	}
}
