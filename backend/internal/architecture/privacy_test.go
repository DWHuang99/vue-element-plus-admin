// T068 privacy scan (contracts/domain-events.md §Principles): payload is
// minimal and excludes passwords, tokens, hashes and unnecessary PII; event
// records are not public HTTP models; correlation_id must not be a raw
// session token; logs may include event ID, type/version, aggregate ID,
// attempt number, correlation ID and safe error code — payload logging
// defaults to disabled.
//
// Static AST scans (same style as architecture_test.go — no go/packages
// dependency). Delivery metadata fields such as claim_token/lease_owner are
// deliberately exempt: the outbox contract (domain-events.md §Outbox
// delivery state) explicitly keeps lease/claim fields as durable fields; the
// scan targets event CONTENT — wire envelope, consumer input, payload
// construction, and log key/value vocabulary.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// secretFieldRe matches names that would carry credentials or PII inside
// event content.
var secretFieldRe = regexp.MustCompile(`(?i)^(pass(word|wd)?|secret|token|hash|salt|email|username|credential|phone|address|password_hash)$`)

// parseGoFile parses one Go source file for AST scans.
func parseGoFile(t *testing.T, rel string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(repoRoot, rel), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return file
}

// structFieldNames returns the field names of a named struct declaration.
func structFieldNames(t *testing.T, file *ast.File, typeName string) []string {
	t.Helper()
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != typeName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("%s is not a struct", typeName)
			}
			var names []string
			for _, f := range st.Fields.List {
				for _, n := range f.Names {
					names = append(names, n.Name)
				}
			}
			return names
		}
	}
	t.Fatalf("struct %s not found", typeName)
	return nil
}

// TestEventContentModelsExcludeSecretFields pins the event content models:
// the wire envelope (integration.Envelope) and the consumer input
// (organization.IAMUserDeletedEvent) must not carry credential/PII fields.
// (Delivery metadata models like iam.ClaimedEvent keep claim_token and
// lease_owner — those are outbox contract fields, not event content.)
func TestEventContentModelsExcludeSecretFields(t *testing.T) {
	for rel, typeName := range map[string]string{
		"internal/integration/event.go":   "Envelope",
		"internal/organization/models.go": "IAMUserDeletedEvent",
	} {
		file := parseGoFile(t, rel)
		for _, field := range structFieldNames(t, file, typeName) {
			if secretFieldRe.MatchString(field) {
				t.Errorf("%s.%s: secret field %q must not exist in event content", rel, typeName, field)
			}
		}
	}
}

// TestUserDeletedPayloadIsMinimal pins the producer side: every
// EnqueueOutboxEvent payload construction in the IAM service is exactly the
// minimal {"user_id": ...} map — no email, username, hash, token or other
// PII enters an outbox row.
func TestUserDeletedPayloadIsMinimal(t *testing.T) {
	file := parseGoFile(t, "internal/iam/service.go")
	checked := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "EnqueueOutboxEvent" {
			return true
		}
		// EnqueueOutboxEvent(ctx, OutboxEventRecord{...}) — find the struct
		// literal argument rather than assuming a fixed index.
		var record *ast.CompositeLit
		for _, arg := range call.Args {
			if cl, ok := arg.(*ast.CompositeLit); ok {
				record = cl
				break
			}
		}
		if record == nil {
			t.Fatal("EnqueueOutboxEvent call has no struct literal argument")
		}
		for _, elt := range record.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Payload" {
				continue
			}
			payload, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				t.Fatal("Payload is not a map literal")
			}
			checked++
			var keys []string
			for _, elt := range payload.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				bl, ok := kv.Key.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				keys = append(keys, mustUnquote(t, bl))
			}
			if len(keys) != 1 || keys[0] != "user_id" {
				t.Errorf("EnqueueOutboxEvent payload keys %v: only user_id is allowed", keys)
			}
			for _, k := range keys {
				if secretFieldRe.MatchString(k) {
					t.Errorf("payload key %q must never appear in an outbox event", k)
				}
			}
		}
		return true
	})
	if checked == 0 {
		t.Fatal("no EnqueueOutboxEvent payload constructions found; check the scan")
	}
}

// TestDispatcherLogsExcludeSensitiveKeys pins the log vocabulary of the
// event-delivery packages (integration dispatcher, platform boundary, IAM
// outbox adapter, Organization inbox consumer): every log call's literal
// key/message strings must avoid credential/PII terms — payload logging
// defaults to disabled and payload content never enters log statements.
func TestDispatcherLogsExcludeSensitiveKeys(t *testing.T) {
	roots := []string{
		"internal/integration",
		"internal/platform",
		"internal/iam/postgres",
		"internal/organization",
	}
	checked := 0
	for _, root := range roots {
		dir := filepath.Join(repoRoot, root)
		entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, path := range entries {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Debug", "Info", "Warn", "Error",
					"DebugContext", "InfoContext", "WarnContext", "ErrorContext":
				default:
					return true
				}
				for _, arg := range call.Args {
					bl, ok := arg.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						continue
					}
					checked++
					lit := mustUnquote(t, bl)
					if secretFieldRe.MatchString(lit) {
						t.Errorf("%s: log literal %q must not carry a credential/PII term", path, lit)
					}
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no log calls scanned; check the scan roots")
	}
}

// TestCorrelationValidationExists pins the static side of
// "correlation_id must not be raw session token": the envelope validator
// keeps a dedicated bounded-identifier regex (no opaque session-token
// shapes) — its rejection behavior is pinned by the T062 fake tests
// (TestDispatch_MalformedEnvelopeBlocks "bad correlation") and its runtime
// property by TestPrivacy_OutboxRowCarriesOnlyMinimalPayload below.
func TestCorrelationValidationExists(t *testing.T) {
	file := parseGoFile(t, "internal/integration/event.go")
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range vs.Names {
			if name.Name != "correlationRe" {
				continue
			}
			call, ok := vs.Values[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "MustCompile" {
				continue
			}
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("correlationRe bounded-identifier regex not found in event.go; correlation_id must never be a raw session token")
	}
}

func mustUnquote(t *testing.T, bl *ast.BasicLit) string {
	t.Helper()
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		t.Fatalf("unquote %s: %v", bl.Value, err)
	}
	return s
}
