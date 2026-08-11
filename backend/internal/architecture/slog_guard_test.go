// slog guards (T072): no package-global slog. slog.Default/slog.SetDefault
// are banned in non-test code, and slog.New is allowed only in the logging
// factory (internal/logging) and the command bootstrap sites (cmd/), which
// own the process-level logger (main.go basicLogger fallback, admin-init
// discard logger). Tests are exempt — they build their own buffer loggers.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// scanSlogCalls returns pkgPath → slog selector calls (New/Default/SetDefault)
// in non-test .go files under internal/ and cmd/ that import log/slog.
func scanSlogCalls(t *testing.T) map[string][]string {
	t.Helper()
	calls := map[string][]string{}

	// parseSlogCalls scans one file, tracking whether it imports log/slog so
	// a shadowed local named "slog" cannot false-positive.
	parseSlogCalls := func(path string) {
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		importsSlog := false
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "log/slog" {
				importsSlog = true
				break
			}
		}
		if !importsSlog {
			return
		}
		rel, rerr := filepath.Rel(repoRoot, filepath.Dir(path))
		if rerr != nil {
			t.Fatalf("rel %s: %v", path, rerr)
		}
		pkg := filepath.ToSlash(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "slog" {
				return true
			}
			switch sel.Sel.Name {
			case "New", "Default", "SetDefault":
				calls[pkg] = append(calls[pkg], sel.Sel.Name)
			}
			return true
		})
	}

	for _, root := range []string{filepath.Join(repoRoot, "internal"), filepath.Join(repoRoot, "cmd")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			parseSlogCalls(path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return calls
}

// TestNoPackageGlobalSlog: slog.Default/SetDefault would install a process
// logger reachable from anywhere; the T072 contract is that every service
// receives its (module-scoped) logger through its constructor instead.
func TestNoPackageGlobalSlog(t *testing.T) {
	calls := scanSlogCalls(t)
	for pkg, names := range calls {
		for _, name := range names {
			if name == "Default" || name == "SetDefault" {
				t.Errorf("%s: package-global slog forbidden (T072): slog.%s used; inject the logger via constructors", pkg, name)
			}
		}
	}
}

// TestSlogNewOnlyInFactoryAndBootstrap: the logging factory builds the
// process logger; cmd/ owns the bootstrap fallback loggers (main.go's
// pre-config basicLogger, admin-init's discard logger). Any other package
// creating a logger itself would bypass level/format config.
func TestSlogNewOnlyInFactoryAndBootstrap(t *testing.T) {
	calls := scanSlogCalls(t)
	for pkg, names := range calls {
		for _, name := range names {
			if name != "New" {
				continue
			}
			allowed := pkg == "internal/logging" || pkg == "cmd/server" || pkg == "cmd/admin-init"
			if !allowed {
				t.Errorf("%s: slog.New only allowed in internal/logging (factory) and cmd/ (bootstrap)", pkg)
			}
		}
	}
}
