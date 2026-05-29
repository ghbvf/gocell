// INVARIANT: PKG-CTXKEYS-NO-CELL-MODEL-01
//
// # PKG-CTXKEYS-NO-CELL-MODEL-01
//
// Invariant: pkg/ctxkeys/ holds ONLY generic cross-service observability and
// networking identifiers (correlation, trace, span, request, real IP).
// Cell-model identifiers — `cell`, `slice`, `journey`, `contract` — must live
// in kernel/ctxkeys/ because they encode GoCell architectural concepts, not
// generic cross-service conventions. This boundary is documented in
// pkg/ctxkeys/doc.go; this archtest is the static guard.
//
// Three Hard layers form a closed funnel; any single one fails CI. Layers
// A/C run in typed mode (RunTyped + TypedOpts{Tests: false}) so that
// (a) the walk naturally excludes _test.go files via Tests=false and
// (b) value-expression resolution covers const-folded forms (BinaryExpr,
// Ident-to-const, cross-package selector) via go/types — not just BasicLit.
//
//   - Layer A — production-AST ↔ golden double-direction diff.
//     Every value-decl name (ValueSpec.Names inside `const` OR `var`),
//     every function name (FuncDecl.Name, including methods), and every
//     type name (TypeSpec.Name) declared in pkg/ctxkeys/ production
//     packages must equal the corresponding golden allowlist exactly.
//     Adding any new identifier — cell-model or not, const or var —
//     requires editing the golden in this file, which forces a reviewer
//     to see the diff. `var` is included alongside `const` because a
//     `var genericKey ctxKey = "cell_id"` declaration would otherwise
//     slip through a const-only walk (closed in PR #1010 round 2).
//
//   - Layer B — golden contents reject cell-model substrings.
//     Each golden allowlist entry is checked (case-insensitive) against
//     {"cell", "slice", "journey", "contract"}. An AI that "just adds the
//     name to the golden" to bypass Layer A still fails Layer B.
//
//   - Layer C — value-expression string values ↔ golden double-direction
//     diff. Defends against `genericKey ctxKey = "cell" + "_id"` shape:
//     an innocuous identifier carrying a cell-model wire value via a
//     constant-folding expression. The set of allowed wire string values
//     is itself a golden, double-direction diffed against actual
//     compile-time string values in const/var ValueSpec.Values, resolved
//     via `EvaluateConstString` (covers BasicLit / Ident / SelectorExpr /
//     BinaryExpr). Non-constant initializers (function calls etc.) yield
//     no value and are skipped — Layer A's name golden still catches the
//     declaration via its identifier name.
//
// Plus one boundary sanity sub-test (Boundary_file_shape_sanity):
//
//   - pkg/ctxkeys/ must not contain a `package ctxkeys_test` file
//     (black-box test packages would bypass the `_test.go` skip in the
//     three layers' production-only walk), must not contain `//go:generate`
//     directives (generated code would bypass the hand-curated golden),
//     and must not contain `//go:build` directives (a file gated out of
//     the default build would skip Layer A/C scanning).
//
// Blind spots within the chosen tool's coverage (per
// .claude/rules/gocell/ai-robust.md §载体决策原则: each must have a reverse
// self-check test asserting it does not occur in production AST):
//
//   - Build-tag-hidden files (//go:build ignore-style gating). The
//     production-only walk parses every .go file in pkg/ctxkeys regardless
//     of build tags, but an AI could add a file with a non-default tag whose
//     contents are skipped by go/build under the standard context.
//     Reverse self-check: Boundary_file_shape_sanity sub-test asserts
//     pkg/ctxkeys/ contains no `//go:build` directive (file-level tag form
//     that would gate the file out of the default build).
//
// Out of rule scope (categorically different rule space, NOT a blind spot
// in the ai-robust sense — no reverse self-check required):
//
//   - Cross-package re-export from another pkg/ subpackage that smuggles
//     a cell-model identifier through pkg/ctxkeys at use-site. The rule
//     scope is pkg/ctxkeys/ declarations; transitive re-export is a
//     separate concern that would need its own archtest.
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Funnel 双向锁评级):
//
//   - Upstream: Medium — production AST → golden diff (Layer A) is
//     archtest-bound, not Go type-system sealed. An AI can edit
//     pkg/ctxkeys/keys.go and the golden in the same PR; protection
//     is detection (Layer A fails until golden updated) plus reviewer
//     diff visibility, not impossibility. Hard upgrade path
//     investigation tracked at gh issue #1012.
//   - Downstream: Hard — any edit to the golden vars (Const/Func/Type
//     names + KeyStringValues) must pass Layer B's case-insensitive
//     cell-model substring check. "Add to golden AND pass B" is a
//     jointly-impossible constraint for cell-model identifiers,
//     closing the only escape path of interest.
//
// ref: tools/archtest/observability_metrics_test.go
// (RunTyped + EvaluateConstString const-fold pattern);
// tools/archtest/governance_rules_invariants_test.go
// (EvaluateConstString for Ident / SelectorExpr / BinaryExpr forms);
// tools/archtest/cells_no_contractspec_import_test.go
// (narrow pkg-boundary scope pattern).
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

const rulePkgCtxkeysNoCellModel = "PKG-CTXKEYS-NO-CELL-MODEL-01"

// cellModelSubstrings is the case-insensitive substring set that names and
// string-literal values in pkg/ctxkeys/ must NOT contain. Mirrors the
// kernel/ctxkeys/ identifier set (cellID, sliceID, journeyID, contractID,
// contractAttrs) at the substring level.
var cellModelSubstrings = []string{"cell", "slice", "journey", "contract"}

// allowedPkgCtxkeysValueDeclNames is the golden allowlist of identifier
// names declared in any value-spec (const OR var) ValueSpec in pkg/ctxkeys/
// production .go files. Source of truth for Layer A's name-diff and Layer B's
// substring check. Any addition requires PR review of this map. `const` is
// the conventional declaration form for context keys (immutable wire
// identifiers); `var` is included only to close the bypass surface — there
// is no legitimate reason to declare a context key as `var`.
var allowedPkgCtxkeysValueDeclNames = map[string]struct{}{
	"correlationID": {},
	"traceID":       {},
	"spanID":        {},
	"traceParent":   {},
	"requestID":     {},
	"realIP":        {},
	"peerIdentity":  {}, // WM-32: mTLS peer X.509 identity (networking, not cell-model)
	// Principal family (issue #1229) — request-scoped OAuth/OIDC identity const
	// keys; see allowedPkgCtxkeysKeyStringValues for the rationale.
	"actorID":   {},
	"subjectID": {},
	"tenantID":  {},
	"sessionID": {},
}

// allowedPkgCtxkeysFuncNames is the golden allowlist of function declaration
// names (including methods — collectPkgCtxkeysIdentifiers walks every
// *ast.FuncDecl.Name regardless of fd.Recv) declared in pkg/ctxkeys/
// production .go files. With/From pair for each of the six const keys.
// No methods exist today; adding one (e.g. `func (k ctxKey) String() string`)
// would require listing the method name here too.
var allowedPkgCtxkeysFuncNames = map[string]struct{}{
	"WithCorrelationID": {}, "CorrelationIDFrom": {},
	"WithTraceID": {}, "TraceIDFrom": {},
	"WithTraceParent": {}, "TraceParentFrom": {},
	"WithSpanID": {}, "SpanIDFrom": {},
	"WithRequestID": {}, "RequestIDFrom": {},
	"WithRealIP": {}, "RealIPFrom": {},
	// WM-32: mTLS peer X.509 identity injected by runtime/http/middleware.MTLS.
	"WithPeerIdentity": {}, "PeerIdentityFrom": {},
	// Principal family (issue #1229) — request-scoped OAuth/OIDC identity
	// accessor pairs; see allowedPkgCtxkeysKeyStringValues for the rationale.
	"WithActorID": {}, "ActorIDFrom": {},
	"WithSubjectID": {}, "SubjectIDFrom": {},
	"WithTenantID": {}, "TenantIDFrom": {},
	"WithSessionID": {}, "SessionIDFrom": {},
}

// allowedPkgCtxkeysTypeNames is the golden allowlist of top-level type
// declarations in pkg/ctxkeys/ production .go files. Only the unexported
// ctxKey alias is permitted.
var allowedPkgCtxkeysTypeNames = map[string]struct{}{
	"ctxKey":       {},
	"PeerIdentity": {}, // WM-32: curated mTLS peer X.509 subset (Subject/DNSNames/URIs)
}

// allowedPkgCtxkeysKeyStringValues is the golden allowlist of string-literal
// values assigned to ctxKey constants. Layer C diffs these against actual
// BasicLit STRING values in const ValueSpec.Values; mismatch in either
// direction fails. Defends against `genericKey ctxKey = "cell_id"` shape.
var allowedPkgCtxkeysKeyStringValues = map[string]struct{}{
	"correlation_id": {},
	"trace_id":       {},
	"span_id":        {},
	"traceparent":    {},
	"request_id":     {},
	"real_ip":        {},
	"peer_identity":  {}, // WM-32: mTLS peer X.509 identity (networking, not cell-model)
	// Principal family (issue #1229): request-scoped OAuth/OIDC identity, set by
	// runtime/auth middleware at the request trust boundary and read by
	// kernel/outbox.ContextPrincipal at NewEntry time. Same category as
	// request_id / correlation_id (request-scoped cross-async identity), NOT a
	// cell-model routing dimension (those — e.g. CellID — live in kernel/ctxkeys).
	"actor_id":   {},
	"subject_id": {},
	"tenant_id":  {},
	"session_id": {},
}

// goldenSelfFile is the module-relative slash path of this file; used as the
// Rel field of "missing from production" diagnostics so the reviewer is
// pointed at the golden var to remove.
const goldenSelfFile = "tools/archtest/pkg_ctxkeys_no_cell_model_test.go"

// foundDecl pairs a discovered identifier or string value with the AST
// location where it was declared, for human-readable diagnostics.
type foundDecl struct {
	rel  string
	line int
}

// TestPkgCtxkeysNoCellModel01 enforces PKG-CTXKEYS-NO-CELL-MODEL-01 across
// three layers (A/B/C) plus one file-shape sanity sub-test. See file header
// godoc for the full rationale and AI-robust framing.
func TestPkgCtxkeysNoCellModel01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	t.Run("LayerA_identifier_golden_diff", func(t *testing.T) {
		t.Parallel()
		diags := RunTyped(t, TypedOpts{Tests: false}, []string{"./pkg/ctxkeys/..."}, func(p *Pass) []Diagnostic {
			actualValueDecls := make(map[string]foundDecl)
			actualFuncs := make(map[string]foundDecl)
			actualTypes := make(map[string]foundDecl)

			for _, file := range p.Files {
				rel := p.Rel(file)
				collectPkgCtxkeysIdentifiers(p.Fset, rel, file, actualValueDecls, actualFuncs, actualTypes)
			}

			var ds []Diagnostic
			ds = append(ds,
				diffNameSet("value-decl", "allowedPkgCtxkeysValueDeclNames", actualValueDecls, allowedPkgCtxkeysValueDeclNames)...)
			ds = append(ds,
				diffNameSet("func", "allowedPkgCtxkeysFuncNames", actualFuncs, allowedPkgCtxkeysFuncNames)...)
			ds = append(ds,
				diffNameSet("type", "allowedPkgCtxkeysTypeNames", actualTypes, allowedPkgCtxkeysTypeNames)...)
			return ds
		})
		Report(t, rulePkgCtxkeysNoCellModel, diags)
	})

	t.Run("LayerB_golden_no_cell_model_substring", func(t *testing.T) {
		t.Parallel()
		var violations []string
		check := func(kind string, set map[string]struct{}) {
			for name := range set {
				lower := strings.ToLower(name)
				for _, sub := range cellModelSubstrings {
					if strings.Contains(lower, sub) {
						violations = append(violations, fmt.Sprintf(
							"%s golden contains cell-model substring %q in %q — "+
								"cell-model identifiers belong in kernel/ctxkeys/, not pkg/ctxkeys/",
							kind, sub, name))
					}
				}
			}
		}
		check("value-decl", allowedPkgCtxkeysValueDeclNames)
		check("func", allowedPkgCtxkeysFuncNames)
		check("type", allowedPkgCtxkeysTypeNames)
		check("key-string-value", allowedPkgCtxkeysKeyStringValues)
		sort.Strings(violations)
		for _, v := range violations {
			t.Errorf("%s: %s", rulePkgCtxkeysNoCellModel, v)
		}
	})

	t.Run("LayerC_key_string_value_golden_diff", func(t *testing.T) {
		t.Parallel()
		diags := RunTyped(t, TypedOpts{Tests: false}, []string{"./pkg/ctxkeys/..."}, func(p *Pass) []Diagnostic {
			actualValues := make(map[string]foundDecl)
			for _, file := range p.Files {
				rel := p.Rel(file)
				collectPkgCtxkeysValueDeclStringValues(p.Fset, p.TypesInfo, rel, file, actualValues)
			}
			return diffStringValueSet(actualValues, allowedPkgCtxkeysKeyStringValues)
		})
		Report(t, rulePkgCtxkeysNoCellModel, diags)
	})

	t.Run("Boundary_file_shape_sanity", func(t *testing.T) {
		t.Parallel()
		// Include _test.go in this sub-scope so a future `package ctxkeys_test`
		// (black-box test) file is visible. Production-only walks in A/C
		// intentionally skip _test.go for golden simplicity; this sub-test
		// closes the gap by refusing the file-shape entirely. Also serves as
		// the reverse self-check for the documented blind spot
		// "build-tag-hidden files" — see file-header godoc.
		boundaryScope := DirsScope(root, []string{"pkg/ctxkeys"}, IncludeTests())
		diags := Run(t, boundaryScope, func(p *Pass) []Diagnostic {
			var ds []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				if file.Name != nil && file.Name.Name == "ctxkeys_test" {
					ds = append(ds, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(file.Name.Pos()).Line,
						Message: "pkg/ctxkeys/ must not contain a `package ctxkeys_test` (black-box) file; " +
							"use white-box test packages so the production-only walk in PKG-CTXKEYS-NO-CELL-MODEL-01 covers it",
					})
				}
				for _, group := range file.Comments {
					for _, c := range group.List {
						switch {
						case strings.HasPrefix(c.Text, "//go:generate"):
							ds = append(ds, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(c.Pos()).Line,
								Message: "pkg/ctxkeys/ must not contain `//go:generate` directives; " +
									"generated code would bypass the hand-curated golden in PKG-CTXKEYS-NO-CELL-MODEL-01",
							})
						case strings.HasPrefix(c.Text, "//go:build"):
							ds = append(ds, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(c.Pos()).Line,
								Message: "pkg/ctxkeys/ must not contain `//go:build` directives; " +
									"a file gated out of the default build would skip Layer A/C scanning " +
									"in PKG-CTXKEYS-NO-CELL-MODEL-01 (build-tag blind-spot reverse self-check)",
							})
						}
					}
				}
			}
			return ds
		})
		Report(t, rulePkgCtxkeysNoCellModel, diags)
	})
}

// collectPkgCtxkeysIdentifiers walks file and accumulates every value-decl
// (token.CONST OR token.VAR) identifier name, every FuncDecl name (including
// methods), and every TypeSpec name into the three out-maps, keyed by name
// with rel/line for diag.
//
// `var` is included alongside `const` so a future `var genericKey ctxKey = ...`
// declaration cannot bypass the golden — the original const-only walk had
// exactly this gap (PR #1010 second-round review).
func collectPkgCtxkeysIdentifiers(
	fset *token.FileSet, rel string, file *ast.File,
	valueDecls, funcs, types map[string]foundDecl,
) {
	EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		switch gd.Tok {
		case token.CONST, token.VAR:
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				for _, name := range vs.Names {
					if name.Name == "_" {
						continue
					}
					valueDecls[name.Name] = foundDecl{rel: rel, line: fset.Position(name.Pos()).Line}
				}
			})
		case token.TYPE:
			EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
				if ts.Name == nil || ts.Name.Name == "_" {
					return
				}
				types[ts.Name.Name] = foundDecl{rel: rel, line: fset.Position(ts.Name.Pos()).Line}
			})
		}
	})
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Name.Name == "_" {
			return
		}
		funcs[fd.Name.Name] = foundDecl{rel: rel, line: fset.Position(fd.Name.Pos()).Line}
	})
}

// collectPkgCtxkeysValueDeclStringValues walks file and accumulates every
// compile-time-constant string value assigned in a const OR var ValueSpec,
// keyed by unquoted value. Layer C diff input.
//
// Value resolution uses [EvaluateConstString] (go/types constant folding),
// which covers BasicLit / Ident / SelectorExpr / BinaryExpr forms. This
// closes the bypass `... = "cell" + "_id"` (binary expr) that the original
// BasicLit-only walk missed (PR #1010 second-round review). Non-constant
// expressions (e.g. `f()`) yield no value and are skipped — Layer A's name
// golden still catches the declaration via its identifier name.
func collectPkgCtxkeysValueDeclStringValues(
	fset *token.FileSet, info *types.Info, rel string, file *ast.File, out map[string]foundDecl,
) {
	EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		if gd.Tok != token.CONST && gd.Tok != token.VAR {
			return
		}
		EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			for _, expr := range vs.Values {
				val, ok := EvaluateConstString(info, expr)
				if !ok {
					continue
				}
				out[val] = foundDecl{rel: rel, line: fset.Position(expr.Pos()).Line}
			}
		})
	})
}

// diffNameSet emits diagnostics for any name in actual not present in allowed
// (extra) and any name in allowed not present in actual (missing). Both
// directions force the golden to stay perfectly aligned with production — a
// silent removal in keys.go can't drift the golden out of sync.
//
// `goldenVarName` is the exact `var ...` identifier in this file so
// diagnostics can point the reviewer at the right map to edit; passed
// explicitly (not derived from kind) so the {kind → golden} mapping stays
// in the call site rather than relying on a name-construction convention.
func diffNameSet(kind, goldenVarName string, actual map[string]foundDecl, allowed map[string]struct{}) []Diagnostic {
	var ds []Diagnostic
	for name, where := range actual {
		if _, ok := allowed[name]; !ok {
			ds = append(ds, Diagnostic{
				Rel:  where.rel,
				Line: where.line,
				Message: fmt.Sprintf(
					"new %s name %q not in PKG-CTXKEYS-NO-CELL-MODEL-01 golden — "+
						"cell-model identifiers belong in kernel/ctxkeys/; if this is "+
						"a legitimate observability/networking key, add it to "+
						"%s in pkg_ctxkeys_no_cell_model_test.go",
					kind, name, goldenVarName),
			})
		}
	}
	goldenLine := goldenVarLine(goldenVarName)
	for name := range allowed {
		if _, ok := actual[name]; !ok {
			ds = append(ds, Diagnostic{
				Rel:  goldenSelfFile,
				Line: goldenLine,
				Message: fmt.Sprintf(
					"%s name %q present in PKG-CTXKEYS-NO-CELL-MODEL-01 golden %s "+
						"but missing from pkg/ctxkeys/ — remove from %s",
					kind, name, goldenVarName, goldenVarName),
			})
		}
	}
	return ds
}

// diffStringValueSet is Layer C's analog of diffNameSet for compile-time
// string values folded out of const/var ValueSpec initializers.
func diffStringValueSet(actual map[string]foundDecl, allowed map[string]struct{}) []Diagnostic {
	var ds []Diagnostic
	for val, where := range actual {
		if _, ok := allowed[val]; !ok {
			ds = append(ds, Diagnostic{
				Rel:  where.rel,
				Line: where.line,
				Message: fmt.Sprintf(
					"value-decl string value %q not in PKG-CTXKEYS-NO-CELL-MODEL-01 golden — "+
						"cell-model wire keys belong in kernel/ctxkeys/; if this is a "+
						"legitimate observability/networking key, add it to "+
						"allowedPkgCtxkeysKeyStringValues",
					val),
			})
		}
	}
	goldenLine := goldenVarLine("allowedPkgCtxkeysKeyStringValues")
	for val := range allowed {
		if _, ok := actual[val]; !ok {
			ds = append(ds, Diagnostic{
				Rel:  goldenSelfFile,
				Line: goldenLine,
				Message: fmt.Sprintf(
					"value-decl string value %q present in PKG-CTXKEYS-NO-CELL-MODEL-01 golden "+
						"but missing from pkg/ctxkeys/ — remove from allowedPkgCtxkeysKeyStringValues",
					val),
			})
		}
	}
	return ds
}

// goldenVarLineCache lazily resolves the declaration line of each
// allowedPkgCtxkeys* golden var inside THIS file. Used by diffNameSet /
// diffStringValueSet so "missing from production" diagnostics point at the
// exact golden var the reviewer must edit, not at file-header line 1.
// Computed once via parseSelf below.
var goldenVarLineCache = sync.OnceValue(parseSelf)

// goldenVarLine returns the AST line of the top-level `var <name> = ...`
// declaration in THIS file, or 1 if the name isn't found (defensive
// fallback; the four golden vars are stable). Looked up via the
// goldenVarLineCache map populated on first call.
func goldenVarLine(name string) int {
	if line, ok := goldenVarLineCache()[name]; ok {
		return line
	}
	return 1
}

// parseSelf parses pkg_ctxkeys_no_cell_model_test.go and returns a map from
// top-level var name to its declaration line. The path is resolved via
// runtime.Caller so the test works regardless of cwd.
func parseSelf() map[string]int {
	out := map[string]int{}
	_, selfPath, _, ok := runtime.Caller(0)
	if !ok {
		return out
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, selfPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return out
	}
	EachInChildren[ast.GenDecl](f, func(gd *ast.GenDecl) {
		if gd.Tok != token.VAR {
			return
		}
		EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			for _, n := range vs.Names {
				out[n.Name] = fset.Position(n.Pos()).Line
			}
		})
	})
	return out
}
