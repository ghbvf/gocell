// invariants:
//   - INVARIANT: HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01
//   - INVARIANT: HEALTH-REDACTED-ERROR-MSG-FUNNEL-01
//
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 — runtime/http/health.verboseDependencyEntry
//
//	is the single source of the /readyz?verbose body dependency entry
//	(json.Marshal of this struct). Two golden literal locks (pure AST, no
//	go/types — the wire shape is a syntactic struct-tag contract):
//	  - TestHealthVerboseWireFieldSetFrozen: field set is exactly
//	    {Status, DurationMs}; embedded fields forbidden.
//	  - TestHealthVerboseWireJSONTagsFrozen: each field's json tag first segment
//	    is frozen to its wire name ({Status:"status", DurationMs:"duration_ms"}).
//	The wire field name is driven by the json tag, NOT the Go field name —
//	locking only the Go field name (the pre-#947 form) let `json:"status"` drift
//	to `json:"state"` undetected. Error text MUST NOT appear here; it belongs to
//	channel d (ops-diagnostics slog). Adding/renaming a field requires updating
//	healthVerboseWireAllowedFields + healthVerboseWireJSONTags and amending ADR
//	docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md
//	§2 D3 (channel mapping) + §4 (enforcement funnel matrix).
//
// HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 — the slog dependency entry error text
//
//	must pass through newRedactedErrorMsg → pkg/redaction.RedactString. Three
//	go/types-resolved guards (RunTyped, not pure AST):
//	  1. TestHealthRedactedErrorMsgConversionFunnel — every redactedErrorMsg(x)
//	     conversion in the package resolves (via info.Types[fun].IsType() +
//	     named-type identity, NOT *ast.Ident name) to this package's
//	     redactedErrorMsg AND sits inside newRedactedErrorMsg's body (downstream
//	     Hard). Scans FuncDecl bodies AND package-level GenDecl initializers
//	     (blind-spot c).
//	  2. TestHealthRedactedErrorMsgFieldTyped — SlogDependencyEntry.errorMsg is
//	     typed redactedErrorMsg (linchpin: a plain-string degrade would let raw
//	     error text populate the field without the redactedErrorMsg(...)
//	     conversion that guard 1 confines).
//	  3. TestHealthRedactedErrorMsgFunnelFuncSig — newRedactedErrorMsg exists
//	     with signature func(error) redactedErrorMsg (anti-vacuous: deleting or
//	     renaming the funnel would make guard 1 pass with zero call sites).
//
//	Why go/types and not pure AST: the pre-#947 rule matched
//	*ast.Ident{Name: "redactedErrorMsg"} only. The unexported newtype +
//	unexported SlogDependencyEntry fields close the UPSTREAM boundary (external
//	packages can name neither the type nor the field — the Go compiler is the
//	gate), but that does NOT stop three in-package regressions: the field
//	degrading to string (guard 2), the funnel function vanishing (guard 3), or a
//	same-named local symbol shadowing the type (guard 1's typed resolution
//	follows the object, not the name). Upstream stays Hard via the type system;
//	downstream is Hard via these three typed guards. There is NO pure-AST
//	"unexported closes the boundary, no go/types needed" shortcut for the
//	downstream gate — that claim (pre-#947 file header) was the bug #947 fixed.
//
// Blind-spot inventory (charter §载体决策原则 mandatory) for the funnel rule:
//
//	(a) external composite-literal SlogDependencyEntry{errorMsg: "raw"} —
//	    compile-time forbidden (unexported field name). In-package, an untyped
//	    string cannot be assigned to the redactedErrorMsg-typed field without a
//	    redactedErrorMsg(...) conversion, which guard 1 confines and guard 2
//	    keeps typed. No extra archtest beyond guard 2.
//	(b) reflect-based construction (reflect.Value.Convert on the unexported
//	    type) — unreachable from outside (type unnameable); in-package reflect is
//	    the bug under investigation, code review is the backstop.
//	(c) package-level GenDecl initializer `var _ = redactedErrorMsg("x")` —
//	    guard 1 scans GenDecl subtrees, not just FuncDecl bodies.
//	(d) alias conversion `type r = redactedErrorMsg; r(x)` — types.Unalias
//	    collapses the alias to the same named type, so guard 1 catches it.
//	(e) funnel deletion/rename → vacuous green — guard 3 fails instead.
//	(f) outflow `string(redactedErrorMsg)` (in LogValue / the ErrorMsg
//	    accessor) — direction is redactedErrorMsg → string, NOT
//	    string → redactedErrorMsg; isRedactedConversion only intercepts inflow
//	    conversions TO the newtype. Outflow is safe by construction (the value
//	    already passed through RedactString before being stored), so guard 1
//	    deliberately does not flag it.
//	(g) test-file `redactedErrorMsg(...)` literals — guard 1 loads with
//	    RunTyped(Tests:false), so verbose_shape_test.go's white-box
//	    redactedErrorMsg("") literals are out of scope by construction.
//	    Switching to Tests:true would require an allowlist for those sites.
//
// Reverse self-check posture (charter §载体决策原则): the wire-shape detection
// logic is exercised by synthetic reverse tests (TestHealthVerboseWire*_Detects*)
// that feed crafted verboseShapeScan values and assert the pure violation
// helpers fire — proving non-vacuity without touching production. The funnel
// rule has NO committed reverse fixture by construction: redactedErrorMsg is
// unexported, so an out-of-funnel conversion is unconstructable in any package
// other than runtime/http/health — the only way to inject one is to mutate that
// package. Non-vacuity is therefore proved by mutation-RED against production
// (recorded in the #947 PR, 5/5 guards red on mutation, green on revert) plus
// the structural anti-vacuous Fatalf in guards 2 & 3. This is the same reason
// PR #552 round-5 deleted its two reverse archtests ("compile-time 已不可表达").
//
// HEALTH-VERBOSE-SCAN-COVERAGE-01 was removed in #947: its purpose (surface a
// type relocation that would let the gates pass vacuously) is now intrinsic to
// each rule — wire-shape Fatalf's when scanVerboseShape doesn't find the struct,
// and the funnel's typed Scope().Lookup Fatalf's when the type / field / funnel
// func is absent. A standalone scope-coverage sanity gate is dead weight.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const (
	ruleHealthVerboseWireShapeFrozen     = "HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01"
	ruleHealthRedactedErrorMsgFunnel     = "HEALTH-REDACTED-ERROR-MSG-FUNNEL-01"
	healthPackageRelativeRoot            = "runtime/http/health"
	healthPackagePattern                 = "./runtime/http/health"
	healthPackageImportPath              = "github.com/ghbvf/gocell/runtime/http/health"
	healthVerboseShapeName               = "verboseDependencyEntry"
	healthSlogShapeName                  = "SlogDependencyEntry"
	healthRedactedErrorMsgTypeName       = "redactedErrorMsg"
	healthRedactedErrorMsgFunnelFuncName = "newRedactedErrorMsg"
	healthRedactedErrorMsgFieldName      = "errorMsg"
)

// healthVerboseWireAllowedFields is the verbatim Go field set of
// runtime/http/health.verboseDependencyEntry. Adding a field requires extending
// this allowlist deliberately and amending ADR 202605171200 §2 D3.
var healthVerboseWireAllowedFields = map[string]struct{}{
	"Status":     {},
	"DurationMs": {},
}

// healthVerboseWireJSONTags is the verbatim json tag (first comma segment) per
// Go field — the actual on-wire field names. Locking the Go field name alone is
// insufficient: the wire name is driven by the json tag.
var healthVerboseWireJSONTags = map[string]string{
	"Status":     "status",
	"DurationMs": "duration_ms",
}

// healthScope returns the DirsScope used by the wire-shape gates. The wire shape
// is a syntactic struct-tag contract, so AST-only Run + DirsScope is sufficient;
// the funnel gates use RunTyped (go/types) instead.
func healthScope(t *testing.T) Scope {
	t.Helper()
	return DirsScope(findModuleRoot(t), []string{healthPackageRelativeRoot})
}

// --- HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 ------------------------------------

// verboseFieldDesc describes one declared field of verboseDependencyEntry.
type verboseFieldDesc struct {
	name    string
	jsonTag string // first comma segment of the json tag; "" if no json tag
	line    int
}

// verboseShapeScan is the result of one scanVerboseShape walk.
type verboseShapeScan struct {
	found    bool
	fields   []verboseFieldDesc
	embedded []int // lines of anonymous/embedded fields (forbidden on the wire)
}

// scanVerboseShape walks runtime/http/health for the verboseDependencyEntry
// struct and returns a description of its declared fields. Shared by both
// wire-shape tests so the AST walk happens once per test (cheap; one ~130-line
// directory), keeping each test a thin single-property assertion.
func scanVerboseShape(t *testing.T) verboseShapeScan {
	t.Helper()
	var scan verboseShapeScan
	_ = Run(t, healthScope(t), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
				collectVerboseSpec(p, ts, &scan)
			})
		}
		return nil
	})
	return scan
}

func collectVerboseSpec(p *Pass, ts *ast.TypeSpec, scan *verboseShapeScan) {
	if ts.Name == nil || ts.Name.Name != healthVerboseShapeName {
		return
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok || st.Fields == nil {
		return
	}
	scan.found = true
	for _, field := range st.Fields.List {
		appendVerboseField(p, field, scan)
	}
}

func appendVerboseField(p *Pass, field *ast.Field, scan *verboseShapeScan) {
	if len(field.Names) == 0 {
		scan.embedded = append(scan.embedded, p.Fset.Position(field.Type.Pos()).Line)
		return
	}
	tag := jsonTagFirstSegment(field.Tag)
	for _, name := range field.Names {
		scan.fields = append(scan.fields, verboseFieldDesc{
			name:    name.Name,
			jsonTag: tag,
			line:    p.Fset.Position(name.Pos()).Line,
		})
	}
}

// jsonTagFirstSegment returns the first comma segment of the field's json tag,
// or "" when the field carries no json tag. ref: golang.org/x/tools
// go/analysis/passes/structtag/structtag.go (reflect.StructTag parsing).
func jsonTagFirstSegment(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	unquoted, err := strconv.Unquote(tag.Value)
	if err != nil {
		return ""
	}
	jsonTag, ok := reflect.StructTag(unquoted).Lookup("json")
	if !ok {
		return ""
	}
	return strings.SplitN(jsonTag, ",", 2)[0]
}

// verboseFieldSetViolations returns the Go-field-set violations of scan against
// healthVerboseWireAllowedFields (embedded field, extra field, missing required
// field). Pure (no *testing.T) so the reverse self-check can exercise it on a
// synthetic scan; the Test funcs map each violation to a t.Errorf.
func verboseFieldSetViolations(scan verboseShapeScan) []string {
	var v []string
	for _, line := range scan.embedded {
		v = append(v, fmt.Sprintf("%s:%d embedded field forbidden — the wire shape carries no "+
			"error text by design (channel d ops-diagnostics owns it)", healthVerboseShapeName, line))
	}
	seen := make(map[string]struct{}, len(scan.fields))
	for _, fld := range scan.fields {
		seen[fld.name] = struct{}{}
		if _, ok := healthVerboseWireAllowedFields[fld.name]; !ok {
			v = append(v, fmt.Sprintf("field %q not in allowlist", fld.name))
		}
	}
	for want := range healthVerboseWireAllowedFields {
		if _, ok := seen[want]; !ok {
			v = append(v, fmt.Sprintf("required field %q missing — removing a field changes the wire payload", want))
		}
	}
	return v
}

// verboseJSONTagViolations returns the json-tag violations of scan against
// healthVerboseWireJSONTags (the actual on-wire field names). Pure (see
// verboseFieldSetViolations rationale). Untracked Go names are skipped —
// field-set drift is verboseFieldSetViolations's job.
func verboseJSONTagViolations(scan verboseShapeScan) []string {
	var v []string
	for _, fld := range scan.fields {
		want, tracked := healthVerboseWireJSONTags[fld.name]
		if tracked && fld.jsonTag != want {
			v = append(v, fmt.Sprintf("field %s json tag = %q, want %q", fld.name, fld.jsonTag, want))
		}
	}
	return v
}

// TestHealthVerboseWireFieldSetFrozen enforces the Go field set half of
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01.
func TestHealthVerboseWireFieldSetFrozen(t *testing.T) {
	t.Parallel()

	scan := scanVerboseShape(t)
	if !scan.found {
		t.Fatalf("%s: %s struct not found under %s — if the type was relocated, update "+
			"healthVerboseShapeName + healthPackageRelativeRoot along with the move",
			ruleHealthVerboseWireShapeFrozen, healthVerboseShapeName, healthPackageRelativeRoot)
	}
	for _, msg := range verboseFieldSetViolations(scan) {
		t.Errorf("%s: %s — adding/removing/renaming a wire field requires updating "+
			"healthVerboseWireAllowedFields + healthVerboseWireJSONTags and amending ADR "+
			"202605171200 §2 D3 + §4", ruleHealthVerboseWireShapeFrozen, msg)
	}
}

// TestHealthVerboseWireJSONTagsFrozen enforces the json-tag half of
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 (the actual on-wire field names).
func TestHealthVerboseWireJSONTagsFrozen(t *testing.T) {
	t.Parallel()

	scan := scanVerboseShape(t)
	if !scan.found {
		t.Fatalf("%s: %s struct not found under %s — if the type was relocated, update "+
			"healthVerboseShapeName + healthPackageRelativeRoot along with the move",
			ruleHealthVerboseWireShapeFrozen, healthVerboseShapeName, healthPackageRelativeRoot)
	}
	for _, msg := range verboseJSONTagViolations(scan) {
		t.Errorf("%s: %s — the wire field name is driven by the json tag, not the Go field "+
			"name; changing it drifts the /readyz?verbose body. Update healthVerboseWireJSONTags "+
			"and amend ADR 202605171200 §2 D3 + §4", ruleHealthVerboseWireShapeFrozen, msg)
	}
}

// TestHealthVerboseWireFieldSetFrozen_DetectsViolation is the reverse self-check
// for verboseFieldSetViolations: a synthetic scan with an extra field + embedded
// field + missing required field must produce violations (non-vacuity), and the
// compliant shape must produce none.
func TestHealthVerboseWireFieldSetFrozen_DetectsViolation(t *testing.T) {
	t.Parallel()

	bad := verboseShapeScan{found: true, embedded: []int{10}, fields: []verboseFieldDesc{{name: "Foo"}}}
	if len(verboseFieldSetViolations(bad)) == 0 {
		t.Errorf("%s: field-set check is vacuous — extra/embedded/missing fields produced no violation",
			ruleHealthVerboseWireShapeFrozen)
	}
	good := verboseShapeScan{found: true, fields: []verboseFieldDesc{{name: "Status"}, {name: "DurationMs"}}}
	if v := verboseFieldSetViolations(good); len(v) != 0 {
		t.Errorf("%s: compliant field set must yield no violations, got %v", ruleHealthVerboseWireShapeFrozen, v)
	}
}

// TestHealthVerboseWireJSONTagsFrozen_DetectsViolation is the reverse self-check
// for verboseJSONTagViolations: a drifted tag must produce a violation, and the
// compliant tags must produce none.
func TestHealthVerboseWireJSONTagsFrozen_DetectsViolation(t *testing.T) {
	t.Parallel()

	bad := verboseShapeScan{found: true, fields: []verboseFieldDesc{{name: "Status", jsonTag: "state"}}}
	if len(verboseJSONTagViolations(bad)) == 0 {
		t.Errorf("%s: json-tag check is vacuous — a drifted tag produced no violation",
			ruleHealthVerboseWireShapeFrozen)
	}
	good := verboseShapeScan{found: true, fields: []verboseFieldDesc{
		{name: "Status", jsonTag: "status"}, {name: "DurationMs", jsonTag: "duration_ms"},
	}}
	if v := verboseJSONTagViolations(good); len(v) != 0 {
		t.Errorf("%s: compliant json tags must yield no violations, got %v", ruleHealthVerboseWireShapeFrozen, v)
	}
}

// --- HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 ------------------------------------

// isHealthRedactedErrorMsgType reports whether t is the runtime/http/health
// redactedErrorMsg newtype, resolved via go/types: types.Unalias makes an alias
// transparent (blind-spot d) and Obj() identity binds it to this package, so a
// same-named type in another package or a local shadow does not match.
func isHealthRedactedErrorMsgType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == healthPackageImportPath &&
		named.Obj().Name() == healthRedactedErrorMsgTypeName
}

// isRedactedConversion reports whether call is a type conversion to
// redactedErrorMsg. info.Types[call.Fun].IsType() distinguishes a conversion
// T(x) from a function call f(x) — ref: golang.org/x/tools go/analysis
// typeutil.Callee returns nil for conversions; this is its dual.
func isRedactedConversion(info *types.Info, call *ast.CallExpr) bool {
	tv, ok := info.Types[call.Fun]
	if !ok || !tv.IsType() {
		return false
	}
	return isHealthRedactedErrorMsgType(tv.Type)
}

// scanFuncDeclConversions flags every redactedErrorMsg(x) conversion inside a
// FuncDecl body other than newRedactedErrorMsg's.
//
// EachInChildren[ast.FuncDecl] (depth-1) suffices: Go's AST places every
// top-level function AND method declaration (incl. ones with a Recv) as a direct
// child of *ast.File; FuncLit closures inside bodies are reached by the inner
// EachInSubtree[ast.CallExpr]. The funnel-skip is a func-name string compare,
// but it is closed-loop, not a Soft anchor: Go forbids two top-level decls
// sharing a name within a package, so the name maps 1:1 to the object, and
// guard 3 (TestHealthRedactedErrorMsgFunnelFuncSig) fails first if that name is
// deleted/renamed — so a rename can never silently re-open this skip.
func scanFuncDeclConversions(p *Pass, f *ast.File) []Diagnostic {
	var ds []Diagnostic
	EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return // bodiless decl (asm / linkname) — no conversion to scan
		}
		if fd.Name.Name == healthRedactedErrorMsgFunnelFuncName {
			return // the sanctioned funnel — conversions here are allowed
		}
		fnName := fd.Name.Name
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if !isRedactedConversion(p.TypesInfo, call) {
				return
			}
			ds = append(ds, Diagnostic{
				Rel:  p.Rel(f),
				Line: p.Fset.Position(call.Pos()).Line,
				Message: fmt.Sprintf(
					"redactedErrorMsg(...) conversion inside func %s; only %s may construct redactedErrorMsg values",
					fnName, healthRedactedErrorMsgFunnelFuncName),
			})
		})
	})
	return ds
}

func scanGenDeclConversions(p *Pass, f *ast.File) []Diagnostic {
	var ds []Diagnostic
	EachInChildren[ast.GenDecl](f, func(gd *ast.GenDecl) {
		EachInSubtree[ast.CallExpr](gd, func(call *ast.CallExpr) {
			if !isRedactedConversion(p.TypesInfo, call) {
				return
			}
			ds = append(ds, Diagnostic{
				Rel:  p.Rel(f),
				Line: p.Fset.Position(call.Pos()).Line,
				Message: fmt.Sprintf(
					"redactedErrorMsg(...) conversion in package-level GenDecl initializer (blind-spot c); "+
						"only %s may construct redactedErrorMsg values", healthRedactedErrorMsgFunnelFuncName),
			})
		})
	})
	return ds
}

// TestHealthRedactedErrorMsgConversionFunnel enforces guard 1 (downstream Hard).
func TestHealthRedactedErrorMsgConversionFunnel(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t, TypedOpts{Tests: false}, []string{healthPackagePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != healthPackageImportPath {
				return nil
			}
			var ds []Diagnostic
			for _, f := range p.Files {
				ds = append(ds, scanFuncDeclConversions(p, f)...)
				ds = append(ds, scanGenDeclConversions(p, f)...)
			}
			return ds
		})

	Report(t, ruleHealthRedactedErrorMsgFunnel, diags)
}

// TestHealthRedactedErrorMsgFieldTyped enforces guard 2 (linchpin).
func TestHealthRedactedErrorMsgFieldTyped(t *testing.T) {
	t.Parallel()

	var checked bool
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{healthPackagePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != healthPackageImportPath {
				return nil
			}
			checked = true
			assertErrorMsgFieldTyped(t, p.Pkg)
			return nil
		})

	if !checked {
		t.Fatalf("%s: package %s not loaded — cannot verify %s.%s field type",
			ruleHealthRedactedErrorMsgFunnel, healthPackageImportPath,
			healthSlogShapeName, healthRedactedErrorMsgFieldName)
	}
}

// assertErrorMsgFieldTyped verifies SlogDependencyEntry.errorMsg is typed
// redactedErrorMsg. A plain-string degrade would let raw error text populate
// the field without the redactedErrorMsg(...) conversion that
// TestHealthRedactedErrorMsgConversionFunnel confines to newRedactedErrorMsg.
func assertErrorMsgFieldTyped(t *testing.T, pkg *types.Package) {
	t.Helper()
	obj := pkg.Scope().Lookup(healthSlogShapeName)
	if obj == nil {
		t.Fatalf("%s: type %s not found in %s",
			ruleHealthRedactedErrorMsgFunnel, healthSlogShapeName, healthPackageImportPath)
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		t.Fatalf("%s: %s is not a struct", ruleHealthRedactedErrorMsgFunnel, healthSlogShapeName)
	}
	field := lookupStructField(st, healthRedactedErrorMsgFieldName)
	if field == nil {
		t.Fatalf("%s: %s has no field %q",
			ruleHealthRedactedErrorMsgFunnel, healthSlogShapeName, healthRedactedErrorMsgFieldName)
	}
	if !isHealthRedactedErrorMsgType(field.Type()) {
		t.Errorf("%s: %s.%s type = %s, want %s.%s — a plain-string degrade makes the redaction funnel vacuous",
			ruleHealthRedactedErrorMsgFunnel, healthSlogShapeName, healthRedactedErrorMsgFieldName,
			field.Type(), healthPackageImportPath, healthRedactedErrorMsgTypeName)
	}
}

func lookupStructField(st *types.Struct, name string) *types.Var {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == name {
			return st.Field(i)
		}
	}
	return nil
}

// TestHealthRedactedErrorMsgFunnelFuncSig enforces guard 3 (anti-vacuous).
func TestHealthRedactedErrorMsgFunnelFuncSig(t *testing.T) {
	t.Parallel()

	var checked bool
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{healthPackagePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != healthPackageImportPath {
				return nil
			}
			checked = true
			assertFunnelFuncSignature(t, p.Pkg)
			return nil
		})

	if !checked {
		t.Fatalf("%s: package %s not loaded — cannot verify %s exists",
			ruleHealthRedactedErrorMsgFunnel, healthPackageImportPath, healthRedactedErrorMsgFunnelFuncName)
	}
}

// assertFunnelFuncSignature verifies newRedactedErrorMsg exists with signature
// func(error) redactedErrorMsg. Deleting or renaming the funnel would make
// TestHealthRedactedErrorMsgConversionFunnel pass vacuously (no conversion call
// sites → no diagnostics); this guard fails instead.
func assertFunnelFuncSignature(t *testing.T, pkg *types.Package) {
	t.Helper()
	obj := pkg.Scope().Lookup(healthRedactedErrorMsgFunnelFuncName)
	if obj == nil {
		t.Fatalf("%s: funnel func %s not found in %s — renaming/deleting it makes the conversion gate vacuous",
			ruleHealthRedactedErrorMsgFunnel, healthRedactedErrorMsgFunnelFuncName, healthPackageImportPath)
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		t.Fatalf("%s: %s is not a function", ruleHealthRedactedErrorMsgFunnel, healthRedactedErrorMsgFunnelFuncName)
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		t.Fatalf("%s: %s has no signature", ruleHealthRedactedErrorMsgFunnel, healthRedactedErrorMsgFunnelFuncName)
	}
	if !funnelSignatureMatches(sig) {
		t.Errorf("%s: %s signature = %s, want func(error) %s",
			ruleHealthRedactedErrorMsgFunnel, healthRedactedErrorMsgFunnelFuncName, sig, healthRedactedErrorMsgTypeName)
	}
}

// funnelSignatureMatches reports whether sig is func(error) redactedErrorMsg.
// types.Universe.Lookup("error").Type() is the predeclared error interface;
// types.Identical compares interfaces structurally (correct regardless of how
// the error reference was spelled at the call site).
func funnelSignatureMatches(sig *types.Signature) bool {
	if sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	if !types.Identical(sig.Params().At(0).Type(), types.Universe.Lookup("error").Type()) {
		return false
	}
	return isHealthRedactedErrorMsgType(sig.Results().At(0).Type())
}
