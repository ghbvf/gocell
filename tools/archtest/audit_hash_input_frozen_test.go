// INVARIANT: AUDIT-HASH-INPUT-FROZEN-01
//
// Locks the canonical 12-field HMAC chain input for the audit ledger so the
// hash-chain format introduced in 043_audit_entries_v2.sql cannot drift via
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
//     encoding/json, the canonical message bytes cannot drift. The first
//     field is Namespace, anchoring each chain to its owner cell so cross-
//     namespace HMAC replay is rejected by signature mismatch.
//
//   - A2 下游 Hard (typed function choice + AST callsite uniqueness with
//     receiver-type lock + typed-resolver alias-proof identification):
//     Within runtime/audit/ledger/*.go (production files), any call to
//     crypto/hmac.New(...) must be located inside the body of the
//     ComputeHash method on a *Protocol receiver. The callee identification
//     uses ResolvePackageRef against types.Info, so import aliases
//     (`import h "crypto/hmac"; h.New(...)`) and dot-imports
//     (`import . "crypto/hmac"; New(...)`) resolve to the same canonical
//     (crypto/hmac, New) tuple as the qualified form — no AST-name bypass
//     remains. The receiver-type check rejects same-package re-shape attacks:
//     a sibling struct adding `func (X) ComputeHash() string` would not
//     satisfy the funnel and would be flagged. Production callers of
//     ComputeHash are MemStore.Append, MemStore.Verify, LedgerStore.Append,
//     and LedgerStore.verifyRange; no other code path may construct an HMAC
//     over audit data. Note: crypto/sha256.New called outside hmac
//     (content-fingerprint in mem_store.go) is intentionally not in scope —
//     that path is a non-HMAC content-id digest, never the chain hash; it
//     cannot drift the chain because the chain-hash funnel routes only
//     through hmac.New.
//
//   - A3 上游 Hard (field-order freeze backing JSON-source-order):
//     encoding/json honors struct source-declaration order. Reordering
//     auditHashInput fields silently changes the canonical bytes written
//     into the HMAC — same data, different hash. A1 reflects field index
//     along with name, so reordering is caught here.
//
//   - B 盲区反向自检 (reverse self-check):
//     three synthetic cases drive the scanner against type-checked source;
//     each case MUST produce a violation. The cases cover:
//     1. bare function (no receiver) outside ComputeHash
//     2. method ComputeHash on a non-Protocol receiver
//     3. import-alias hmac.New (proves typed-resolver alias-proofing)
//     The test fails if any case silently passes, proving the scanner can
//     distinguish allowed vs. forbidden callsites across all three vectors.
//
// Funnel cross-link:
//   - Sibling: PRINCIPAL-SEALED-FIELD-FROZEN-01 (PR-A2, outbox.Entry
//     wire envelope) — outbox-side envelope freeze.
//   - Conformance: storetest.RunPrincipalFieldsRoundTrip + the in-package
//     12-field tamper test prove behavior against every locked field.
//
// ref ADR-1042 (docs/architecture/202605281200-1042-*.md) §Decision 4 audit
// ledger HMAC msg rewrite + §Decision 6 archtest funnel.
// ref issue #1228 — PR #1218 withdrawal + audit_entries v2 rebuild.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

const ruleAuditHashInputFrozen01 = "AUDIT-HASH-INPUT-FROZEN-01"

// auditHashInputField captures the canonical (name, json tag, Go type) for one
// field of runtime/audit/ledger.auditHashInput. Reordering the slice changes
// the canonical HMAC message bytes — encoding/json emits struct fields in
// source-declaration order — so the order here is part of the lock.
type auditHashInputField struct {
	Name    string
	JSONTag string
	GoType  string
}

// expectedAuditHashInputFields is the authoritative 12-field canonical input
// for the audit ledger HMAC chain. Namespace is field 1 (cross-namespace
// domain separation; ref: google/trillian TreeID participation in
// SignedEntryTimestamp). Any change requires an ADR amendment (ADR
// 202605281200-1042-...md §Decision 4) and migration of every stored hash
// (which today means a DROP+CREATE of audit_entries, per #1228).
var expectedAuditHashInputFields = []auditHashInputField{
	{Name: "Namespace", JSONTag: "namespace", GoType: "string"},
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
// to the canonical 12 fields. Drift in any of {field set, order, name, JSON
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
	if len(got) != 12 {
		t.Errorf("AUDIT-HASH-INPUT-FROZEN-01 A1: expected exactly 12 fields, got %d", len(got))
	}
}

// TestAuditHashInputFrozen_A2_HmacCallsite locks crypto/hmac.New calls within
// runtime/audit/ledger production source to the body of Protocol.ComputeHash.
// The callee identification uses go/types via ResolvePackageRef, so an import
// alias cannot bypass the lock.
func TestAuditHashInputFrozen_A2_HmacCallsite(t *testing.T) {
	t.Parallel()
	Report(t, ruleAuditHashInputFrozen01+"/A2", CheckAuditHashInputFrozenA2(t, ConfigForExternalCell{}))
}

// TestAuditHashInputFrozen_B_ReverseSelfCheck proves the A2 scanner
// distinguishes allowed vs. forbidden hmac.New callsites across three vectors.
// Each case feeds a synthetic source file through the type-checker (so
// ResolvePackageRef resolves real crypto/hmac.New) and runs the same A2 rule.
// If any case fails to produce a violation the scanner is broken (or the
// alias-proof guarantee silently regressed).
func TestAuditHashInputFrozen_B_ReverseSelfCheck(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "bare_function_outside_ComputeHash_fires",
			src: `package fakeledger

import (
	"crypto/hmac"
	"crypto/sha256"
)

func notComputeHash(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("synthetic"))
	return mac.Sum(nil)
}
`,
		},
		{
			name: "ComputeHash_on_wrong_receiver_fires",
			src: `package fakeledger

import (
	"crypto/hmac"
	"crypto/sha256"
)

type Imposter struct{}

func (Imposter) ComputeHash(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("imposter"))
	return mac.Sum(nil)
}
`,
		},
		{
			name: "import_alias_hmac_fires",
			src: `package fakeledger

import (
	h "crypto/hmac"
	"crypto/sha256"
)

func aliasViolation(key []byte) []byte {
	mac := h.New(sha256.New, key)
	mac.Write([]byte("alias"))
	return mac.Sum(nil)
}
`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := scanSyntheticSource(t, tc.src)
			if len(diags) == 0 {
				t.Errorf("AUDIT-HASH-INPUT-FROZEN-01 B/%s: scanner did not fire", tc.name)
			}
		})
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
	ts, found := FindFirstInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) bool {
		return ts.Name != nil && ts.Name.Name == "auditHashInput"
	})
	if !found {
		t.Fatal("AUDIT-HASH-INPUT-FROZEN-01: auditHashInput type not found in protocol.go")
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok || st.Fields == nil {
		t.Fatalf("AUDIT-HASH-INPUT-FROZEN-01: auditHashInput must be a struct type")
	}
	fields := collectStructFields(t, fset, st)
	if len(fields) == 0 {
		t.Fatal("AUDIT-HASH-INPUT-FROZEN-01: auditHashInput type not found in protocol.go")
	}
	return fields
}

// collectStructFields walks struct fields and returns name/jsonTag/goType for
// each. Helper split out of collectAuditHashInputFields to keep cognitive
// complexity below the project ceiling.
func collectStructFields(t *testing.T, fset *token.FileSet, st *ast.StructType) []auditHashInputField {
	t.Helper()
	var fields []auditHashInputField
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
	return fields
}

// exprToString renders an ast.Expr as its canonical Go source form for the
// limited shapes used by auditHashInput fields.
func exprToString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.ArrayType:
		if v.Len != nil {
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
