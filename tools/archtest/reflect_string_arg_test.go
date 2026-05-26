package archtest

// reflect_string_arg_test.go — single source for the reflect.Value
// {Field,Method}ByName string-argument scanner shared by every reflect
// blind-spot reverse self-check, plus its own reverse self-check fixture.
//
// INVARIANT: REFLECT-STRING-ARG-SCANNER-01
//
// Category (ai-robust.md §"工具选定后强制盲区自检"): the reflect/unsafe
// blind-spot tests are the MANDATED reverse self-checks that supply the
// prerequisite evidence for the Hard primary field-access funnels (typed
// *types.Info.Selections resolution). They are not standalone Soft invariants
// subject to the ≥Medium 立项 threshold; the charter REQUIRES them to exist.
//
// Grade — Medium (Soft→Medium upgrade, ai-robust.md "既有 Soft → 升级 Medium,
// 不在 Soft 层打补丁"): scanReflectStringArgCalls identifies a violation through
// TYPE information, not a string anchor —
//
//	1. typed receiver: ResolveMethodCall must resolve the call to a method of
//	   reflect.Value (pkg=="reflect", receiver type=="Value"). This excludes
//	   non-reflect types that happen to expose a FieldByName/MethodByName
//	   method, and reflect.Type.FieldByName (which returns StructField metadata
//	   and cannot read a field VALUE — not a bypass vector; see the legitimate
//	   use in contract_subscribers_funnel_test.go).
//	2. typed arg: EvaluateConstString folds the single argument (covers raw
//	   string / const ident / concatenation / cross-package const), replacing
//	   the incumbent *ast.BasicLit + strings.Trim that was blind to all but the
//	   plain double-quoted literal (issue #948 / PR #542).
//
// Why not Hard (upgrade path exhausted): reflect.Value.FieldByName(runtimeVar)
// with a non-constant name cannot be folded → invisible to any static scan
// (the irreducible reflect caveat, shared by all reflect scans). A blanket
// reflect-import ban (the Hard form, mirroring the sibling unsafe-import ban)
// is infeasible because the scanned scopes (cells/ + runtime/ + cmd/)
// legitimately use reflect. Medium is the reachable ceiling.
//
// This is a shared scanning helper, NOT a funnel (it neither restricts who may
// call a method nor forces callers through a single path), so ai-robust.md
// §"Funnel 双向锁评级" does not apply; each consuming archtest carries its own
// funnel grading in its own godoc.
//
// Out-of-scope reflect read shapes (documented blind spots — this scanner keys
// on the string ARGUMENT of FieldByName/MethodByName, so it cannot see):
//   - reflect.Value.FieldByIndex([]int{…}) / .FieldByIndexErr(…) / .Field(i) —
//     positional field read, no string name. Index→field-name resolution needs
//     the reflected struct type + numeric-const fold + (for non-inline) dataflow.
//   - reflect.Value.Method(i) — positional method, same shape.
// These are pre-existing gaps in all reflect blind-spot scans, not introduced
// or widened here; closing them is a separate (harder, false-positive-prone)
// effort tracked in #1120 — NOT a 立项-able Soft string-anchor.
//
// Blind-spot reverse self-check: TestReflectStringArgScanner_TypedReceiverAndConstArg
// loads testdata/reflect_string_form_red and asserts the three const-form
// FieldByName + three const-form MethodByName calls are detected, AND that the
// two boundary calls (runtime-value arg; non-reflect receiver) are NOT — pinning
// both Medium boundaries as tested facts.
//
// Residual gap (not Hard-closable, not a 立项-able Soft guard): a NEW blind-spot
// test re-inlining BasicLit extraction instead of calling this helper. A clean
// guard is infeasible (archtest itself has legitimate reflect.Type.FieldByName
// uses, so distinguishing "blind-spot scan" from "legit reflection" needs
// fragile semantic adjacency = Soft, which ai-robust.md forbids 立项). Mitigated
// by single-sourcing here + this meta-test (guards the helper from regressing)
// + review. Documented, not silently carried.

import (
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

// reflect method names + the receiver-type identity, hoisted to constants
// because they recur across the ten reverse-self-check call sites.
const (
	reflectFieldByName  = "FieldByName"
	reflectMethodByName = "MethodByName"
	reflectPkgPath      = "reflect"
	reflectValueType    = "Value"
)

// reflectStringArgHit is one detected reflect.Value.{Field,Method}ByName call
// whose constant-folded string argument is in the caller's banned set.
type reflectStringArgHit struct {
	Line int
	Name string // the constant-folded argument value
}

// scanReflectStringArgCalls walks file for calls to reflect.Value.<method>
// (method ∈ {"FieldByName","MethodByName"}) whose single argument is a banned
// constant string. See the package-level INVARIANT doc for the type-aware
// two-gate design and grading. The sel.Sel.Name == method check is only a cheap
// pre-filter; the real gate is isReflectValueMethod (typed receiver) +
// EvaluateConstString (typed arg).
func scanReflectStringArgCalls(p *Pass, file *ast.File, method string, banned func(string) bool) []reflectStringArgHit {
	var out []reflectStringArgHit
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != method {
			return
		}
		if len(call.Args) != 1 {
			return
		}
		if !isReflectValueMethod(p.TypesInfo, sel) {
			return
		}
		name, ok := EvaluateConstString(p.TypesInfo, call.Args[0])
		if !ok || !banned(name) {
			return
		}
		out = append(out, reflectStringArgHit{
			Line: p.Fset.Position(call.Pos()).Line,
			Name: name,
		})
	})
	return out
}

// isReflectValueMethod reports whether sel typed-resolves to a method of
// reflect.Value (pkg "reflect", receiver named type "Value"). This is the typed
// receiver gate that separates a genuine reflect bypass from a non-reflect type
// exposing a same-named method and from reflect.Type.FieldByName metadata reads.
func isReflectValueMethod(info *types.Info, sel *ast.SelectorExpr) bool {
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != reflectPkgPath {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	// typeOwner (shared archtest helper in helpers_test.go) unwraps *T→T to the
	// owning *types.TypeName.
	owner := typeOwner(sig.Recv().Type())
	return owner != nil && owner.Name() == reflectValueType
}

// TestReflectStringArgScanner_TypedReceiverAndConstArg is the reverse
// self-check for REFLECT-STRING-ARG-SCANNER-01. It loads the shared RED fixture
// and asserts exact hit counts of 4 (3 method-value const-forms — raw/const/
// concat — plus 1 method-expression form), simultaneously proving the positive
// forms AND excluding every boundary call (any boundary leaking in would push
// the count to 5+):
//   - method expression: reflect.Value.FieldByName(recv, "X") puts the name at
//     Args[1] (Go spec §Method expressions); the incumbent len(Args)!=1 scan
//     missed it. This is the count's 4th member.
//   - receiver boundary (non-reflect): fakeReflect{}.FieldByName("RevokedAt") —
//     excluded by the reflect.Value-only receiver gate.
//   - receiver boundary (reflect.Type): reflect.TypeOf(x).FieldByName("X")
//     returns metadata, not a value — excluded (Type, not Value).
//   - arg boundary (runtime-value): FieldByName(runtimeName) folds to no
//     constant (EvaluateConstString → false), so it never enters the banned
//     check.
func TestReflectStringArgScanner_TypedReceiverAndConstArg(t *testing.T) {
	t.Parallel()

	// RunTyped (not a fixture-tagged loader): reflect_string_form_red is a
	// testdata/ subpackage of the main module — excluded from `go build ./...`
	// and `./...` patterns, loaded only via this explicit path. It lives under
	// cells/accesscore/ because it imports internal/domain (see fixture godoc).
	const fixture = "./cells/accesscore/internal/credentialauthority/testdata/reflect_string_form_red"

	var fieldHits, methodHits []reflectStringArgHit
	_ = RunTyped(t, TypedOpts{}, []string{fixture}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		// No _test.go guard: TypedOpts{} defaults Tests:false, so p.Files holds
		// only the fixture's non-test source.
		for _, file := range p.Files {
			fieldHits = append(fieldHits, scanReflectStringArgCalls(p, file, reflectFieldByName,
				func(n string) bool { return n == credRevokedAt })...)
			methodHits = append(methodHits, scanReflectStringArgCalls(p, file, reflectMethodByName,
				func(n string) bool { return n == credCanAuthenticate })...)
		}
		return nil
	})

	assert.Len(t, fieldHits, 4,
		"REFLECT-STRING-ARG-SCANNER-01: FieldByName(RevokedAt) must detect exactly the 3 "+
			"method-value forms (raw/const/concat) + 1 method-expression form — not the "+
			"runtime-value arg, not the non-reflect receiver, not reflect.Type. Got %d.",
		len(fieldHits))
	assert.Len(t, methodHits, 4,
		"REFLECT-STRING-ARG-SCANNER-01: MethodByName(CanAuthenticate) must detect exactly the 3 "+
			"method-value forms + 1 method-expression form. Got %d.", len(methodHits))
}
