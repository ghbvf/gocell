// INVARIANT: AUDIT-HASH-INPUT-FROZEN-01
//
// Locks the canonical 11-field HMAC chain input for the audit ledger so the
// hash-chain format introduced in 041_audit_entries_v2.sql cannot drift via
// either an upstream rewrite of the typed input struct or a downstream
// alternative-callsite that constructs an HMAC over audit data outside the
// single sanctioned Protocol.ComputeHash entry point.
//
// AI-robust 评级 (per .claude/rules/gocell/ai-robust.md §Hard 范本目录):
//
//   - A1 上游 Hard (sealed construction + AST/reflect field lock):
//     runtime/audit/ledger.auditHashInput is package-private (unexported
//     type name). External packages can neither construct nor declare an
//     alias / re-shape, so the type identity is closed at compile time.
//     Inside the package, this archtest pins field set + order + Go type +
//     JSON struct tag via AST, so a same-package rename or field reordering
//     fails immediately. Combined with the JSON-source-order determinism of
//     encoding/json, the canonical message bytes cannot drift.
//
//   - A2 下游 Hard (typed function choice + AST callsite uniqueness):
//     Within runtime/audit/ledger/*.go (production files), any call to
//     hmac.New(...) must be located inside the body of the exported
//     ComputeHash method on *Protocol. No helper, no test seam, no
//     "tamper utility" gets to re-derive the HMAC outside the single
//     sanctioned funnel; if it did, the message format could disagree with
//     auditHashInput silently. Production callers of the funnel are
//     enumerated as `(MemStore.Append, MemStore.Verify, LedgerStore.Append,
//     LedgerStore.verifyRange)` via Protocol.ComputeHash; no other code path
//     may construct an HMAC over audit data.
//
//   - A3 上游 Hard (field-order freeze backing JSON-source-order):
//     encoding/json honors struct source-declaration order. Reordering
//     the auditHashInput fields silently changes the canonical bytes
//     written into the HMAC — same data, different hash. A1 reflects field
//     index along with name, so reordering is caught here.
//
//   - B 盲区反向自检 (reverse self-check):
//     a synthetic source file containing a hmac.New call outside ComputeHash
//     drives the A2 scanner under a temp directory; the scanner MUST produce
//     a violation. The test fails if the reverse check silently passes,
//     proving the scanner can distinguish allowed vs. forbidden callsites.
//
// Funnel cross-link:
//   - Sibling: PRINCIPAL-SEALED-FIELD-FROZEN-01 (PR-A2, outbox.Entry
//     wire envelope) — outbox-side envelope freeze.
//   - Conformance: storetest.RunPrincipalFieldsRoundTrip + the in-package
//     11-field tamper test prove behavior against every locked field.
//
// ref ADR-1042 (docs/architecture/202605281200-1042-*.md) §Decision 4 audit
// ledger HMAC msg rewrite + §Decision 6 archtest funnel.
// ref issue #1228 — PR #1218 withdrawal + audit_entries v2 rebuild.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// auditHashInputField captures the canonical (name, json tag, Go type) for one
// field of runtime/audit/ledger.auditHashInput. Reordering the slice changes
// the canonical HMAC message bytes — encoding/json emits struct fields in
// source-declaration order — so the order here is part of the lock.
type auditHashInputField struct {
	Name    string
	JSONTag string
	GoType  string
}

// expectedAuditHashInputFields is the authoritative 11-field canonical input
// for the audit ledger HMAC chain. Any change requires an ADR amendment
// (ADR 202605281200-1042-...md §Decision 4) and migration of every stored
// hash (which today means a DROP+CREATE of audit_entries, per #1228).
var expectedAuditHashInputFields = []auditHashInputField{
	{Name: "PrevHash", JSONTag: "prev_hash", GoType: "string"},
	{Name: "EventID", JSONTag: "event_id", GoType: "string"},
	{Name: "EventType", JSONTag: "event_type", GoType: "string"},
	{Name: "ActorID", JSONTag: "actor_id", GoType: "string"},
	{Name: "SubjectID", JSONTag: "subject_id", GoType: "string"},
	{Name: "TenantID", JSONTag: "tenant_id", GoType: "string"},
	{Name: "SessionID", JSONTag: "session_id", GoType: "string"},
	{Name: "CorrelationID", JSONTag: "correlation_id", GoType: "string"},
	{Name: "OccurredAtUnixNano", JSONTag: "occurred_at_unix_nano", GoType: "int64"},
	{Name: "TimestampUnixNano", JSONTag: "timestamp_unix_nano", GoType: "int64"},
	{Name: "Payload", JSONTag: "payload", GoType: "[]byte"},
}

// TestAuditHashInputFrozen_A1_StructShape locks runtime/audit/ledger.auditHashInput
// to the canonical 11 fields. Drift in any of {field set, order, name, JSON
// tag, Go type} surfaces as a test failure pointing at the specific field.
func TestAuditHashInputFrozen_A1_StructShape(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	protocolPath := filepath.Join(root, "runtime", "audit", "ledger", "protocol.go")

	got := collectAuditHashInputFields(t, protocolPath)
	if !reflect.DeepEqual(got, expectedAuditHashInputFields) {
		t.Errorf("AUDIT-HASH-INPUT-FROZEN-01 A1: auditHashInput shape drifted.\n"+
			"  got  %#v\n  want %#v\n"+
			"If intentional, update expectedAuditHashInputFields + amend ADR 1042 §Decision 4 "+
			"+ regenerate every persisted hash (chain format changed).",
			got, expectedAuditHashInputFields)
	}
	if len(got) != 11 {
		t.Errorf("AUDIT-HASH-INPUT-FROZEN-01 A1: expected exactly 11 fields, got %d", len(got))
	}
}

// TestAuditHashInputFrozen_A2_HmacCallsite locks hmac.New calls within
// runtime/audit/ledger production source to the body of Protocol.ComputeHash.
// Any other function (helper, test seam in production source, sibling method
// that re-derives the HMAC) is rejected.
func TestAuditHashInputFrozen_A2_HmacCallsite(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	ledgerDir := filepath.Join(root, "runtime", "audit", "ledger")

	violations := scanHmacNewCallsitesOutsideComputeHash(t, ledgerDir)
	if len(violations) > 0 {
		t.Errorf("AUDIT-HASH-INPUT-FROZEN-01 A2: hmac.New called outside Protocol.ComputeHash.\n"+
			"  violations: %v\n"+
			"All audit HMAC computation must flow through Protocol.ComputeHash "+
			"with the same auditHashInput marshaling. If you need a new HMAC primitive "+
			"(e.g. content fingerprint), introduce a typed funnel and extend "+
			"this archtest's allowlist explicitly.",
			violations)
	}
}

// TestAuditHashInputFrozen_B_ReverseSelfCheck proves that the A2 scanner
// distinguishes allowed vs. forbidden hmac.New callsites: a synthetic file
// outside ComputeHash MUST produce a violation. If the reverse self-check
// passes silently the scanner is broken.
func TestAuditHashInputFrozen_B_ReverseSelfCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Synthesize a production-like file with a forbidden hmac.New callsite
	// (outside any function named ComputeHash).
	src := `package fakeledger

import (
	"crypto/hmac"
	"crypto/sha256"
)

func notComputeHash(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("synthetic"))
	return mac.Sum(nil)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write synthetic source: %v", err)
	}

	violations := scanHmacNewCallsitesOutsideComputeHash(t, dir)
	if len(violations) == 0 {
		t.Error("AUDIT-HASH-INPUT-FROZEN-01 B: reverse self-check did not fire — scanner cannot tell allowed vs. forbidden callsites")
	}
}

// collectAuditHashInputFields parses protocolPath, locates the auditHashInput
// struct typespec, and returns its fields in source-declaration order.
func collectAuditHashInputFields(t *testing.T, protocolPath string) []auditHashInputField {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, protocolPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("AUDIT-HASH-INPUT-FROZEN-01: parse %s: %v", protocolPath, err)
	}

	var fields []auditHashInputField
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "auditHashInput" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			t.Fatalf("AUDIT-HASH-INPUT-FROZEN-01: auditHashInput must be a struct type")
		}
		for _, f := range st.Fields.List {
			if f.Tag == nil {
				t.Fatalf("AUDIT-HASH-INPUT-FROZEN-01: field without JSON tag at %v", fset.Position(f.Pos()))
			}
			tag, err := strconv.Unquote(f.Tag.Value)
			if err != nil {
				t.Fatalf("AUDIT-HASH-INPUT-FROZEN-01: unquote tag %q: %v", f.Tag.Value, err)
			}
			jsonTag := reflect.StructTag(tag).Get("json")
			goType := exprToString(f.Type)
			for _, name := range f.Names {
				fields = append(fields, auditHashInputField{
					Name:    name.Name,
					JSONTag: jsonTag,
					GoType:  goType,
				})
			}
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatal("AUDIT-HASH-INPUT-FROZEN-01: auditHashInput type not found in protocol.go")
	}
	return fields
}

// scanHmacNewCallsitesOutsideComputeHash walks dir, parses each *.go (skipping
// _test.go), and returns "file:line" strings for hmac.New(...) calls that are
// not inside a function declaration whose name is exactly "ComputeHash".
func scanHmacNewCallsitesOutsideComputeHash(t *testing.T, dir string) []string {
	t.Helper()
	var violations []string
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Don't recurse into subdirectories (storetest / etc. live under
			// their own funnels; archtest scopes A2 to the canonical ledger
			// package source).
			if path == dir {
				return nil
			}
			return filepath.SkipDir
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("AUDIT-HASH-INPUT-FROZEN-01 A2: parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Body == nil {
				continue
			}
			isComputeHash := fn.Name != nil && fn.Name.Name == "ComputeHash"
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if !isHmacNewSelector(call.Fun) {
					return true
				}
				if isComputeHash {
					return true
				}
				violations = append(violations,
					fset.Position(call.Pos()).Filename+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
				return true
			})
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("AUDIT-HASH-INPUT-FROZEN-01 A2: walk %s: %v", dir, walkErr)
	}
	return violations
}

// isHmacNewSelector reports whether expr is the selector hmac.New (with any
// package alias resolved by its identifier name). We accept both the canonical
// `hmac.New` and `hmac.New(...)` chain heads — only the selector identity
// matters; argument shapes are policed by Go's type checker at compile time.
func isHmacNewSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel == nil || sel.Sel.Name != "New" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "hmac"
}

// exprToString renders an ast.Expr as its canonical Go source form for the
// limited shapes used by auditHashInput fields. Anything more exotic than
// what the canonical struct uses is flagged by the test guard.
func exprToString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.ArrayType:
		if v.Len != nil {
			// Unexpected: auditHashInput fields are not fixed-length arrays.
			return "<unexpected-fixed-array>"
		}
		return "[]" + exprToString(v.Elt)
	case *ast.SelectorExpr:
		pkg, _ := v.X.(*ast.Ident)
		if pkg == nil {
			return "<unexpected-selector>"
		}
		return pkg.Name + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(v.X)
	}
	return "<unsupported>"
}
