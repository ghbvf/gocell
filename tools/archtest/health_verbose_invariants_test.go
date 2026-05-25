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
//	TestHealthVerboseWireMapsParity enforces that the two allowlist maps hold the
//	identical key set, so a field added to one but not the other cannot slip its
//	json tag past the lock.
//
// HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 — the slog dependency entry error text
//
//	must pass through newRedactedErrorMsg → pkg/redaction.RedactString. Four
//	go/types-resolved guards (RunTyped, not pure AST):
//	  1. TestHealthRedactedErrorMsgCreationFunnel — every redactedErrorMsg value
//	     CREATED in the package is created inside newRedactedErrorMsg's body span.
//	     A value is created by either an explicit conversion redactedErrorMsg(x)
//	     (info.Types[fun].IsType() + named-type identity, NOT *ast.Ident name) OR
//	     a constant of type redactedErrorMsg (info.Types[e].Value != nil — an
//	     untyped string acquiring the type from context: literal, const-ident,
//	     const concat, paren — ANY AST form). The rule ITERATES info.Types (the
//	     type checker's complete expr→type record), so it has no per-AST-node-kind
//	     blind spot — closing the const-ident inflow (#996 review) that a
//	     BasicLit/CallExpr walk missed. A typed string is not assignable without a
//	     conversion, so constant-inflow + conversion is the complete creation set.
//	     Funnel membership is by source-position span, not a func-name skip
//	     (downstream Hard, F1 #947).
//	  2. TestHealthRedactedErrorMsgFieldTyped — SlogDependencyEntry.errorMsg is
//	     typed redactedErrorMsg (linchpin: a plain-string degrade would let raw
//	     error text populate the field without any redactedErrorMsg creation).
//	  3. TestHealthRedactedErrorMsgFunnelFuncSig — newRedactedErrorMsg exists
//	     with signature func(error) redactedErrorMsg (anti-vacuous: deleting or
//	     renaming the funnel would make guard 1 pass with zero creation sites).
//	  4. TestHealthRedactedErrorMsgFunnelBodyRedacts — inside newRedactedErrorMsg,
//	     every redactedErrorMsg(x) conversion's argument is a
//	     pkg/redaction.RedactString(...) call (F2 #947). Guards 1-3 confine and
//	     pin the funnel but DON'T verify the body redacts — without guard 4,
//	     returning raw err.Error() keeps all of them green. Form-uniqueness lock
//	     (the canonical redactedErrorMsg(redaction.RedactString(...)) shape).
//
//	Why go/types and not pure AST: the pre-#947 rule matched
//	*ast.Ident{Name: "redactedErrorMsg"} only. The unexported newtype +
//	unexported SlogDependencyEntry fields close the UPSTREAM boundary (external
//	packages can name neither the type nor the field — the Go compiler is the
//	gate), but that does NOT stop four in-package regressions: untyped-const
//	inflow without a conversion CallExpr (guard 1's info.Types constant scan,
//	any AST form), the field degrading to string (guard 2), the funnel function
//	vanishing (guard 3), the body dropping RedactString (guard 4), or a
//	same-named local symbol shadowing the type (guard 1's typed resolution
//	follows the object, not the name).
//	Upstream stays Hard via the type system; downstream is Hard via these four
//	typed guards. There is NO pure-AST "unexported closes the boundary, no
//	go/types needed" shortcut for the downstream gate.
//
// Blind-spot inventory (charter §载体决策原则 mandatory) for the funnel rule:
//
//	(a) untyped-const inflow in ANY AST form — composite-literal field
//	    (SlogDependencyEntry{errorMsg: "raw"}), var/const init (var x
//	    redactedErrorMsg = "raw"), return, assignment, AND const-ident /
//	    const-concat indirection (const c = "raw"; var _ redactedErrorMsg = c).
//	    EXTERNAL packages cannot name the unexported field/type (compiler gate),
//	    but IN-PACKAGE an untyped string constant IS implicitly converted with NO
//	    conversion CallExpr (legal Go — the pre-F1 "cannot be assigned without a
//	    conversion" claim was WRONG). Guard 1 iterates info.Types and flags every
//	    constant of type redactedErrorMsg regardless of AST node kind, so this is
//	    a CLOSED gap, not a blind spot (the #996 review-reported const-ident
//	    bypass is caught — the const's use site is a constant redactedErrorMsg
//	    expr in info.Types).
//	(b) reflect-based construction (reflect.Value.Convert on the unexported
//	    type) — unreachable from outside (type unnameable); in-package reflect is
//	    a bypass-only construct (no normal code does this), code review is the
//	    backstop. Soft → Hard-upgrade path tracked in gh issue #999.
//	(c) package-level GenDecl initializer `var _ = redactedErrorMsg("x")` /
//	    `var _ redactedErrorMsg = "x"` — guard 1's info.Types iteration covers
//	    package-level decls too (no FuncDecl-body restriction); funnel membership
//	    is by source-position span.
//	(d) alias conversion `type r = redactedErrorMsg; r(x)` — types.Unalias
//	    collapses the alias to the same named type, so guard 1 catches it.
//	(e) funnel deletion/rename → vacuous green — guard 3 fails instead.
//	(f) outflow `string(redactedErrorMsg)` (in LogValue / the ErrorMsg
//	    accessor) — direction is redactedErrorMsg → string, NOT
//	    string → redactedErrorMsg; isRedactedConversion only intercepts inflow
//	    conversions TO the newtype. Outflow is safe by construction (the value
//	    already passed through RedactString before being stored), so guard 1
//	    deliberately does not flag it. (Freezing the LogValue slog-key literals
//	    {status,duration_ms,error_msg} is a separate enforcement gap tracked in
//	    gh issue #998 — out of #947 scope.)
//	(g) test-file `redactedErrorMsg(...)` literals — guard 1 loads with
//	    RunTyped(Tests:false), so verbose_shape_test.go's white-box
//	    redactedErrorMsg("") literals are out of scope by construction.
//	    Switching to Tests:true would require an allowlist for those sites.
//	(h) generic conversion laundering — `func g[T ~string](s string) T {
//	    return T(s) }; g[redactedErrorMsg]("raw")`: inside g the conversion is
//	    typed as the type parameter T (not redactedErrorMsg), and the call result
//	    is a non-constant func-call value, so neither guard-1 branch fires. Like
//	    (b), this is a bypass-only construct — no normal code defines a generic
//	    string-newtype launderer; adding one is itself the bug. Residual,
//	    code-review backstop. (Distinct from the former (i) const-ident case,
//	    which was a NORMAL idiom and is now CLOSED — see (a).)
//
// Reverse self-check posture (charter §载体决策原则): the wire-shape detection
// logic is exercised by synthetic reverse tests (TestHealthVerboseWire*_Detects*)
// that feed crafted verboseShapeScan values and assert the pure violation
// helpers fire — proving non-vacuity without touching production. The funnel
// rule has NO committed reverse fixture by construction: redactedErrorMsg is
// unexported, so an out-of-funnel creation (conversion or untyped-const inflow)
// is unconstructable in any package other than runtime/http/health — the only
// way to inject one is to mutate that package. Non-vacuity is therefore proved
// by mutation-RED against production (recorded in the #947 PR, 7/7 guards red on
// mutation incl. const-inflow + RedactString-drop, green on revert) plus the
// structural anti-vacuous Fatalf in guards 2/3/4. This is the same reason PR
// #552 round-5 deleted its two reverse archtests ("compile-time 已不可表达").
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
	"go/token"
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
	redactionPkgPath                     = "github.com/ghbvf/gocell/pkg/redaction"
	redactionPkgName                     = "redaction"
	redactStringFuncName                 = "RedactString"
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
	rel      string // module-relative path of the file declaring the struct
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
			rel := p.Rel(f)
			EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
				collectVerboseSpec(p, ts, rel, &scan)
			})
		}
		return nil
	})
	return scan
}

func collectVerboseSpec(p *Pass, ts *ast.TypeSpec, rel string, scan *verboseShapeScan) {
	if ts.Name == nil || ts.Name.Name != healthVerboseShapeName {
		return
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok || st.Fields == nil {
		return
	}
	scan.found = true
	scan.rel = rel
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
// field) as Diagnostics carrying the production rel/line. Pure (no *testing.T)
// so the reverse self-check can exercise it on a synthetic scan; the Test funcs
// pass the result to Report.
func verboseFieldSetViolations(scan verboseShapeScan) []Diagnostic {
	var ds []Diagnostic
	for _, line := range scan.embedded {
		ds = append(ds, Diagnostic{
			Rel: scan.rel, Line: line,
			Message: "embedded field forbidden — the wire shape carries no error text by design " +
				"(channel d ops-diagnostics owns it)",
		})
	}
	seen := make(map[string]struct{}, len(scan.fields))
	for _, fld := range scan.fields {
		seen[fld.name] = struct{}{}
		if _, ok := healthVerboseWireAllowedFields[fld.name]; !ok {
			ds = append(ds, Diagnostic{
				Rel: scan.rel, Line: fld.line,
				Message: fmt.Sprintf("field %q not in allowlist — adding/renaming a wire field requires updating "+
					"healthVerboseWireAllowedFields + healthVerboseWireJSONTags and amending ADR 202605171200 §2 D3 + §4",
					fld.name),
			})
		}
	}
	for want := range healthVerboseWireAllowedFields {
		if _, ok := seen[want]; !ok {
			ds = append(ds, Diagnostic{
				Rel: scan.rel, Line: 0,
				Message: fmt.Sprintf("required field %q missing — removing a field changes the wire payload", want),
			})
		}
	}
	return ds
}

// verboseJSONTagViolations returns the json-tag violations of scan against
// healthVerboseWireJSONTags (the actual on-wire field names) as Diagnostics.
// Untracked Go names are skipped — field-set drift is verboseFieldSetViolations's
// job, and allowedFields↔jsonTags key parity is TestHealthVerboseWireMapsParity's.
func verboseJSONTagViolations(scan verboseShapeScan) []Diagnostic {
	var ds []Diagnostic
	for _, fld := range scan.fields {
		want, tracked := healthVerboseWireJSONTags[fld.name]
		if tracked && fld.jsonTag != want {
			ds = append(ds, Diagnostic{
				Rel: scan.rel, Line: fld.line,
				Message: fmt.Sprintf("field %s json tag = %q, want %q — the wire field name is driven by the json "+
					"tag, not the Go field name; update healthVerboseWireJSONTags and amend ADR 202605171200 §2 D3 + §4",
					fld.name, fld.jsonTag, want),
			})
		}
	}
	return ds
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
	Report(t, ruleHealthVerboseWireShapeFrozen, verboseFieldSetViolations(scan))
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
	Report(t, ruleHealthVerboseWireShapeFrozen, verboseJSONTagViolations(scan))
}

// TestHealthVerboseWireMapsParity enforces that healthVerboseWireAllowedFields and
// healthVerboseWireJSONTags hold the identical key set (F3 #947). Without this, a
// field added to the allowlist but not the tag map would pass the field-set gate
// yet have its json tag silently unlocked (verboseJSONTagViolations skips
// untracked names).
func TestHealthVerboseWireMapsParity(t *testing.T) {
	t.Parallel()

	for name := range healthVerboseWireAllowedFields {
		if _, ok := healthVerboseWireJSONTags[name]; !ok {
			t.Errorf("%s: field %q is in healthVerboseWireAllowedFields but missing from "+
				"healthVerboseWireJSONTags — its json tag would be unlocked", ruleHealthVerboseWireShapeFrozen, name)
		}
	}
	for name := range healthVerboseWireJSONTags {
		if _, ok := healthVerboseWireAllowedFields[name]; !ok {
			t.Errorf("%s: field %q is in healthVerboseWireJSONTags but missing from "+
				"healthVerboseWireAllowedFields", ruleHealthVerboseWireShapeFrozen, name)
		}
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

// isRedactedCreation reports whether expr (carrying TypeAndValue tv) CREATES a
// redactedErrorMsg value: either a constant of type redactedErrorMsg (untyped
// string inflow in ANY AST form — literal, const-ident, const concat, paren) or
// an explicit redactedErrorMsg(x) conversion CallExpr. Reads (field / variable /
// parameter references of that type) are non-constant non-conversions and are
// NOT creations. Evaluated off info.Types entries, so there is no per-AST-node-
// kind blind spot (closes blind-spots a + i: literal AND const-ident inflow).
func isRedactedCreation(info *types.Info, expr ast.Expr, tv types.TypeAndValue) bool {
	if !isHealthRedactedErrorMsgType(tv.Type) {
		return false
	}
	if tv.Value != nil {
		return true // constant inflow — any AST form (literal / const-ident / concat)
	}
	call, ok := expr.(*ast.CallExpr)
	return ok && isRedactedConversion(info, call)
}

// funnelBodyRange returns the [lo, hi] source span of newRedactedErrorMsg's body
// (plus whether it was found). Creation points inside the span are the sanctioned
// funnel; everything else is a violation. Position-based, not a func-name string
// skip — the only sanctioned creation site is literally the funnel body's source
// range, so a rename can never silently re-open it (guard 3 also fails on rename).
func funnelBodyRange(p *Pass) (lo, hi token.Pos, found bool) {
	for _, f := range p.Files {
		EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Body != nil && fd.Name.Name == healthRedactedErrorMsgFunnelFuncName {
				lo, hi, found = fd.Body.Pos(), fd.Body.End(), true
			}
		})
	}
	return lo, hi, found
}

// fileRelIndex maps an absolute filename (as Fset.Position reports) to its
// module-relative slash path, so map-iterated diagnostics carry rel/line.
func fileRelIndex(p *Pass) func(string) string {
	idx := make(map[string]string, len(p.Files))
	for _, f := range p.Files {
		idx[p.Fset.Position(f.Pos()).Filename] = p.Rel(f)
	}
	return func(filename string) string {
		if rel, ok := idx[filename]; ok {
			return rel
		}
		return filename
	}
}

// redactedCreationViolations iterates every typed expression in the package and
// flags each redactedErrorMsg creation point whose source position is outside the
// funnel body span [lo, hi]. Iterating info.Types (the type checker's complete
// expr→type record) is exhaustive over AST node kinds — unlike a BasicLit/CallExpr
// walk it cannot miss const-ident or const-concat inflows. Standard go/types,
// not ast.Inspect (SCANNER-FRAMEWORK-USAGE-01) nor typeseval helpers
// (PASS-FUNNEL-RESOLVE-01). Map order is non-deterministic; Report sorts.
func redactedCreationViolations(p *Pass, lo, hi token.Pos) []Diagnostic {
	relOf := fileRelIndex(p)
	var ds []Diagnostic
	for expr, tv := range p.TypesInfo.Types {
		if !isRedactedCreation(p.TypesInfo, expr, tv) {
			continue
		}
		if lo <= expr.Pos() && expr.Pos() <= hi {
			continue // inside the funnel body — the sole sanctioned creation site
		}
		pos := p.Fset.Position(expr.Pos())
		ds = append(ds, Diagnostic{
			Rel:  relOf(pos.Filename),
			Line: pos.Line,
			Message: fmt.Sprintf("%s outside %s; only the funnel may create redactedErrorMsg values",
				redactedCreationForm(tv), healthRedactedErrorMsgFunnelFuncName),
		})
	}
	return ds
}

func redactedCreationForm(tv types.TypeAndValue) string {
	if tv.Value != nil {
		return "untyped-constant inflow to redactedErrorMsg"
	}
	return "redactedErrorMsg(...) conversion"
}

// TestHealthRedactedErrorMsgCreationFunnel enforces guard 1 (downstream Hard):
// every redactedErrorMsg value CREATED in the package is created inside
// newRedactedErrorMsg — explicit conversions AND untyped-constant inflows in any
// AST form (F1 #947; const-ident closed via info.Types iteration, #996 review).
func TestHealthRedactedErrorMsgCreationFunnel(t *testing.T) {
	t.Parallel()

	var funnelFound bool
	diags := RunTyped(t, TypedOpts{Tests: false}, []string{healthPackagePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != healthPackageImportPath {
				return nil
			}
			lo, hi, ok := funnelBodyRange(p)
			funnelFound = ok
			return redactedCreationViolations(p, lo, hi)
		})

	Report(t, ruleHealthRedactedErrorMsgFunnel, diags)
	if !funnelFound {
		t.Fatalf("%s: funnel func %s not found in %s — cannot anchor the creation-funnel span",
			ruleHealthRedactedErrorMsgFunnel, healthRedactedErrorMsgFunnelFuncName, healthPackageImportPath)
	}
}

// TestHealthRedactedErrorMsgFunnelBodyRedacts enforces guard 4 (F2 #947): inside
// newRedactedErrorMsg, every redactedErrorMsg(x) conversion's argument must be a
// pkg/redaction.RedactString(...) call. Guards 1-3 confine creation to the funnel
// and pin its signature/field type, but say nothing about whether the body
// actually redacts — dropping RedactString (returning raw err.Error()) keeps all
// of them green. This guard locks the canonical form redactedErrorMsg(
// redaction.RedactString(...)); a local-var refactor intentionally fails it
// (form-uniqueness, the typed-marker-funnel Hard template) and must keep the
// canonical shape or update this guard.
func TestHealthRedactedErrorMsgFunnelBodyRedacts(t *testing.T) {
	t.Parallel()

	var checked bool
	diags := RunTyped(t, TypedOpts{Tests: false}, []string{healthPackagePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != healthPackageImportPath {
				return nil
			}
			var ds []Diagnostic
			for _, f := range p.Files {
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Body == nil || fd.Name.Name != healthRedactedErrorMsgFunnelFuncName {
						return
					}
					checked = true
					ds = append(ds, funnelBodyRedactViolations(p, f, fd.Body)...)
				})
			}
			return ds
		})

	Report(t, ruleHealthRedactedErrorMsgFunnel, diags)
	if !checked {
		t.Fatalf("%s: funnel func %s not found in %s — cannot verify its body redacts",
			ruleHealthRedactedErrorMsgFunnel, healthRedactedErrorMsgFunnelFuncName, healthPackageImportPath)
	}
}

// funnelBodyRedactViolations flags each redactedErrorMsg(x) conversion in body
// whose argument is not a redaction.RedactString(...) call.
func funnelBodyRedactViolations(p *Pass, f *ast.File, body ast.Node) []Diagnostic {
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if !isRedactedConversion(p.TypesInfo, call) {
			return
		}
		if !argIsRedactString(p.TypesInfo, call) {
			ds = append(ds, Diagnostic{
				Rel:  p.Rel(f),
				Line: p.Fset.Position(call.Pos()).Line,
				Message: fmt.Sprintf("redactedErrorMsg conversion argument must be %s.RedactString(...); "+
					"the funnel must redact before wrapping", redactionPkgName),
			})
		}
	})
	return ds
}

// argIsRedactString reports whether call is redactedErrorMsg(redaction.RedactString(...)):
// exactly one argument that is itself a CallExpr resolving (typed) to
// pkg/redaction.RedactString.
func argIsRedactString(info *types.Info, call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	inner, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(info, inner.Fun)
	return ok && pkgPath == redactionPkgPath && name == redactStringFuncName
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
