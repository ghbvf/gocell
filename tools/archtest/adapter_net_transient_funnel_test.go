// INVARIANT: ADAPTER-NET-TRANSIENT-FUNNEL-01
//   - INVARIANT: TRANSIENT-NET-HELPER-FORM-01
//
// Hard double-lock for the adapter "net.Error → transient" rule
// (ai-robust.md §AI-robust + §Funnel 双向锁 + ADR 202605161800):
//
//   - Downstream Hard (ADAPTER-NET-TRANSIENT-FUNNEL-01): every site in
//     production code that performs a net.Error fan-in decision must reside
//     in an allowlisted (pkgPath, funcName) pair. Two equivalent shapes are
//     locked:
//     (a) `var x net.Error` declaration (allowlist detector,
//     scanNetErrorDeclarations; alias-aware via types.Unalias).
//     (b) `errors.As(err, &op)` where op has type `*net.X` for some
//     concrete X != "Error" (narrow-form detector,
//     scanErrorsAsNetSubtypeNarrow). Adapters cannot bypass the
//     interface-level check by anchoring on a concrete subtype.
//     Everywhere outside the allowlist must delegate to
//     errcode.IsTransientNet. Allowlist drift requires same-PR edit to
//     this file + ADR §"Adapter transient inventory".
//
//   - Upstream Hard (TRANSIENT-NET-HELPER-FORM-01): the body of
//     pkg/errcode.IsTransientNet is locked to a positive + negative
//     compound form:
//     Positive (scanHelperPositiveErrorsAs): body MUST contain at least
//     one stdlib `errors.As(<expr>, &<ident>)` CallExpr where the ident
//     has the static type net.Error interface. Empty / stub bodies
//     (`return false` / `return true`) are RED.
//     Negative (scanHelperFormViolations): `.Timeout()` SelectorExpr on
//     a net.Error receiver is RED (would narrow transient set);
//     `*net.X` StarExpr where X != "Error" is RED (would key on a
//     concrete subtype instead of the interface). `*url.Error` is in
//     a different package and not net.*, so url.Error unwrap
//     preamble is permitted (matches the helper's documented shape).
//
// Tool: archtest.RunTypedProduction (040 Pass-Driver) + *types.Info Uses /
// TypeOf for callee + type resolution; AST walk via EachInSubtree[ast.GenDecl] /
// EachInSubtree[ast.SelectorExpr] / EachInSubtree[ast.CallExpr]; alias
// transparency via types.Unalias.
//
// Declared blind spots (ai-robust.md §"工具选定后强制盲区自检"):
//
//  1. Reflective type-name comparison ("net.Error" string-equality): impossible
//     for in-package var declarations — Go type system requires `var x net.Error`
//     to import `net`, which `*types.Info.ObjectOf(spec.Type.Sel).Pkg().Path()`
//     resolves authoritatively. Compensation: Go type system.
//  2. Type alias `type myNetErr = net.Error; var x myNetErr`: detected via
//     `types.Unalias` in `isNetErrorTypeExpr` and
//     `isExprStaticallyNetError`. Reverse fixture `forbiddenAliasNetError`
//     verifies the path catches the alias declaration. Compensation:
//     types.Unalias + reverse fixture.
//  3. Short-decl `x := someFunc()` whose type is net.Error: very rare in
//     practice; the canonical Go pattern is `var x net.Error` zero-value-
//     then-As. The narrow-form detector (`scanErrorsAsNetSubtypeNarrow`)
//     scans every `errors.As(_, &x)` call regardless of declaration form,
//     so short-decl narrow forms (`x := (*net.OpError)(nil); errors.As(...)`)
//     would still be caught. Pure interface short-decl is functionally
//     dead code (zero value of interface = nil) and not a real bypass.
//     Compensation: errors.As-callsite type check.
//  4. Body-scope detector that bypasses enclosing function (e.g., closure
//     captured `net.Error` from outer scope): the declaration site is what we
//     scan; the enclosing function name is captured via positional containment.
//     A closure assigning into an outer `var netErr net.Error` would still be
//     anchored to the outer function's declaration. Compensation: positional
//     enclosing-func scan locks the declaration site, not the assignment site.
//  5. `errors.As` callee bypass (alternative function with the same shape
//     that traverses the chain similarly): the positive scan resolves the
//     callee via `*types.Info` to stdlib "errors".As exactly; a fork or
//     wrapper would not satisfy the positive check, forcing the regression
//     RED. The negative scans on the helper body still trigger on
//     `.Timeout()` / `*net.X` regardless of where the call originates.
//
// Reverse self-check: TestADAPTER_NET_TRANSIENT_FUNNEL_01_FixturePattern
// loads tools/archtest/internal/nettransientfunnelfixture/ via
// RunTypedFixture and asserts the synthetic regressed forms are reported,
// while the clean form is not. Bypassing the reverse self-check requires
// editing the real fixture source.
//
// File-naming note: this file declares 2 related rules sharing the
// net.Error transient funnel theme (ADAPTER-NET-TRANSIENT-FUNNEL-01 +
// TRANSIENT-NET-HELPER-FORM-01). Per ai-robust.md §archtest 文件命名,
// the `_invariants_test.go` rename is triggered once a third related
// rule lands. Until then this single-file form is intentional.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// netErrorAllowlist is the authoritative (pkgPath suffix → funcName set)
// mapping of production sites legitimately declaring `var x net.Error`
// outside the errcode.IsTransientNet funnel. Each entry is documented in
// ADR docs/architecture/202605161800-adr-adapter-error-classification.md
// §"Adapter transient inventory". Adding an entry requires same-PR ADR
// amendment.
//
// Keys are package path suffixes (joined to the module root at test time).
// Values are sets of allowed top-level function names.
var netErrorAllowlist = map[string]map[string]struct{}{
	// errcode.IsTransientNet — the funnel helper itself (TRANSIENT-NET-HELPER-FORM-01).
	// errcode.IsTransient   — umbrella predicate's Tier 2 raw-error branch
	//                         keeps Timeout()-only semantics intentionally
	//                         conservative for unclassified errors.
	"/pkg/errcode": {
		"IsTransientNet": {},
		"IsTransient":    {},
	},
	// postgres isRetryablePGError — Timeout() fallback after pgconn.SafeToRetry
	//                               handles dial-refused; widening would change
	//                               query-path retry semantics.
	// postgres isConnectTimeout   — Timeout-specific helper for substituting
	//                               ErrAdapterPGConnectTimeout code; NOT a
	//                               general transient classifier.
	"/adapters/postgres": {
		"isRetryablePGError": {},
		"isConnectTimeout":   {},
	},
	// rabbitmq classifyStructuredDialError — Timeout() discriminates dialClass
	//                                        (both branches transient,
	//                                        Timeout sub-branch substitutes
	//                                        ErrAdapterAMQPConnectTimeout).
	"/adapters/rabbitmq": {
		"classifyStructuredDialError": {},
	},
	// vault classifyAuthLoginError — Timeout() discriminates reasonTimeout vs
	//                                reasonNetwork metric label, both routed
	//                                separately for ops triage.
	"/adapters/vault": {
		"classifyAuthLoginError": {},
	},
}

// helperFormFuncName is the name of the funnel helper whose body is locked
// by TRANSIENT-NET-HELPER-FORM-01.
const helperFormFuncName = "IsTransientNet"

// helperFormPkgSuffix is the package-path suffix that contains the funnel
// helper.
const helperFormPkgSuffix = "/pkg/errcode"

func TestADAPTER_NET_TRANSIENT_FUNNEL_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	allowed := resolveAllowlist(modPath, netErrorAllowlist)

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		var ds []Diagnostic
		ds = append(ds, scanNetErrorDeclarations(p, allowed)...)
		ds = append(ds, scanErrorsAsNetSubtypeNarrow(p, allowed)...)
		return ds
	})
	Report(t, "ADAPTER-NET-TRANSIENT-FUNNEL-01", diags)
}

func TestTRANSIENT_NET_HELPER_FORM_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	helperPkgPath := modPath + helperFormPkgSuffix

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		if p.Pkg.Path() != helperPkgPath {
			return nil
		}
		return scanHelperFormViolations(p, helperFormFuncName)
	})
	Report(t, "TRANSIENT-NET-HELPER-FORM-01", diags)
}

// TestADAPTER_NET_TRANSIENT_FUNNEL_01_FixturePattern is the reverse self-check
// for both detectors: a build-tag-gated package whose forbidden / regressed
// shapes MUST be reported, and whose allowed / clean shapes MUST NOT.
func TestADAPTER_NET_TRANSIENT_FUNNEL_01_FixturePattern(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkgPath := modPath + "/tools/archtest/internal/nettransientfunnelfixture"
	fixturePattern := "./tools/archtest/internal/nettransientfunnelfixture/..."

	// Fixture-local allowlist: allow only "allowedSite"; "forbiddenSite" /
	// "regressedHelperTimeout" / "regressedHelperNarrow" must be reported.
	fixtureAllowed := map[string]map[string]struct{}{
		fixturePkgPath: {"allowedSite": {}},
	}

	// Allowlist detector: 4 RED expected — forbiddenSite +
	// regressedHelperTimeout + regressedHelperNarrow + forbiddenAliasNetError
	// (the last one uses `type myNetErr = net.Error`, verifying the
	// types.Unalias resolution path; without Unalias it slips past).
	allowlistDiags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			return scanNetErrorDeclarations(p, fixtureAllowed)
		})

	for _, d := range allowlistDiags {
		t.Logf("allowlist: %s", d.Message)
	}
	require.Len(t, allowlistDiags, 4,
		"fixture allowlist detector must yield exactly 4 RED sites "+
			"(forbiddenSite + regressedHelperTimeout + regressedHelperNarrow + "+
			"forbiddenAliasNetError); allowedSite must NOT be flagged")
	joined := ""
	for _, d := range allowlistDiags {
		joined += d.Message + "\n"
	}
	assert.Contains(t, joined, "forbiddenSite")
	assert.Contains(t, joined, "regressedHelperTimeout")
	assert.Contains(t, joined, "regressedHelperNarrow")
	assert.Contains(t, joined, "forbiddenAliasNetError")
	assert.NotContains(t, joined, "allowedSite")

	// Narrow-form detector (scanErrorsAsNetSubtypeNarrow): 2 RED expected —
	// regressedHelperNarrow (contains `var op *net.OpError; errors.As(err, &op)`)
	// + forbiddenOpErrorNarrow (same shape). The function-name match excludes
	// allowedSite (no narrow form) and forbiddenSite (no narrow form).
	narrowFormDiags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			return scanErrorsAsNetSubtypeNarrow(p, fixtureAllowed)
		})
	for _, d := range narrowFormDiags {
		t.Logf("narrow-form: %s", d.Message)
	}
	require.Len(t, narrowFormDiags, 2,
		"narrow-form detector must yield exactly 2 RED sites "+
			"(regressedHelperNarrow + forbiddenOpErrorNarrow); both contain "+
			"`var op *net.OpError; errors.As(err, &op)` outside the allowlist")
	joinedNarrow := ""
	for _, d := range narrowFormDiags {
		joinedNarrow += d.Message + "\n"
	}
	assert.Contains(t, joinedNarrow, "regressedHelperNarrow")
	assert.Contains(t, joinedNarrow, "forbiddenOpErrorNarrow")

	// Positive helper-form check: empty-body regressions must be reported.
	// scanHelperPositiveErrorsAs requires the named function body to contain
	// at least one `errors.As(err, &netErrVar)` call with the var typed
	// net.Error; absence is RED.
	for _, regressed := range []string{"regressedHelperEmptyFalse", "regressedHelperEmptyTrue"} {
		regressed := regressed
		emptyDiags := RunTypedFixture(t, FixtureOpts{Tests: false},
			[]string{fixturePattern},
			func(p *Pass) []Diagnostic {
				if p.Pkg == nil || p.TypesInfo == nil {
					return nil
				}
				if p.Pkg.Path() != fixturePkgPath {
					return nil
				}
				return scanHelperFormViolations(p, regressed)
			})
		for _, d := range emptyDiags {
			t.Logf("helper-form positive %s: %s", regressed, d.Message)
		}
		require.NotEmpty(t, emptyDiags,
			"positive shape check must flag empty/stub body for "+regressed+
				" — body must contain canonical `errors.As(err, &netErrVar)`")
	}

	// Helper-form detector: targeting the synthetic "regressedHelperTimeout"
	// function name; expect 1 RED diag (Timeout SelectorExpr present).
	helperDiags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			return scanHelperFormViolations(p, "regressedHelperTimeout")
		})

	for _, d := range helperDiags {
		t.Logf("helper-form regressedHelperTimeout: %s", d.Message)
	}
	require.NotEmpty(t, helperDiags,
		"helper-form detector must flag the Timeout() filter regression "+
			"in regressedHelperTimeout body")

	// Helper-form detector: targeting the synthetic "regressedHelperNarrow"
	// function name; expect 1 RED diag (*net.OpError narrowing present).
	// Verifies each regressed form independently, each verified independently
	// (asserts the detector catches both regressed forms, each verified independently).
	narrowDiags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			return scanHelperFormViolations(p, "regressedHelperNarrow")
		})
	for _, d := range narrowDiags {
		t.Logf("helper-form regressedHelperNarrow: %s", d.Message)
	}
	require.NotEmpty(t, narrowDiags,
		"helper-form detector must flag the *net.OpError narrowing regression "+
			"in regressedHelperNarrow body")

	// Also confirm the clean shape (allowedSite) is NOT flagged when targeted.
	cleanDiags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			return scanHelperFormViolations(p, "allowedSite")
		})
	for _, d := range cleanDiags {
		t.Logf("helper-form allowedSite: %s", d.Message)
	}
	require.Empty(t, cleanDiags,
		"helper-form detector must NOT flag the canonical "+
			"`var n net.Error; return errors.As(err, &n)` shape")
}

// resolveAllowlist joins package-suffix keys to the module root, producing a
// `fullPkgPath → funcName set` map suitable for direct lookup against
// p.Pkg.Path().
func resolveAllowlist(modPath string, in map[string]map[string]struct{}) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{}, len(in))
	for suffix, fns := range in {
		out[modPath+suffix] = fns
	}
	return out
}

// scanNetErrorDeclarations reports every `var x net.Error` declaration in p
// whose enclosing top-level function is NOT in the allowlist for p.Pkg.Path().
//
// Detection: walk `*ast.GenDecl` (Tok==VAR) and `*ast.ValueSpec` to find any
// declaration whose Type expression resolves (via *types.Info.TypeOf) to the
// stdlib net.Error interface (matched by package path "net" + type name
// "Error"). Anonymous / package-scope sites use the sentinel "<package>".
func scanNetErrorDeclarations(p *Pass, allowlist map[string]map[string]struct{}) []Diagnostic {
	allowed, tracked := allowlist[p.Pkg.Path()]
	// Untracked packages: no constraint applied — every `var x net.Error` site
	// passes silently. Tracked packages: every declaration must be in `allowed`.
	if !tracked {
		// However, untracked production packages OUTSIDE the adapter / errcode
		// scope might still introduce `var x net.Error`. We default to "no
		// declarations permitted in untracked packages" so a future adapter
		// (e.g. adapters/kafka) inheriting a `var x net.Error` form is RED in
		// CI, forcing the author to either use the helper or extend the
		// allowlist with an ADR amendment.
		// For now we apply the rule only to known scopes (allowlist entries +
		// adapters/ + pkg/errcode); other packages pass.
		if !inEnforcementScope(p.Pkg.Path()) {
			return nil
		}
		allowed = map[string]struct{}{}
	}

	var ds []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
			if gd.Tok != token.VAR {
				return
			}
			// EachInChildren[ast.ValueSpec] is the typed depth-1 walk over
			// GenDecl.Specs (SCANNER-FRAMEWORK-USAGE-01 funnel; the
			// `for _, spec := range gd.Specs` + type-assertion shape is RED).
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				if vs.Type == nil {
					return
				}
				if !isNetErrorTypeExpr(p.TypesInfo, vs.Type) {
					return
				}
				fn := enclosingFuncName(file, vs.Pos())
				if fn == "" {
					fn = "<package scope>"
				}
				if _, ok := allowed[fn]; ok {
					return
				}
				ds = append(ds, Diagnostic{
					Rel:  p.Rel(file),
					Line: p.Fset.Position(vs.Pos()).Line,
					Message: fmt.Sprintf(
						"`var %s net.Error` declared in %s.%s is outside the "+
							"ADAPTER-NET-TRANSIENT-FUNNEL-01 allowlist; route the "+
							"net.Error transient decision through "+
							"errcode.IsTransientNet, or — for legitimate "+
							"non-transient uses (Timeout-only code substitution, "+
							"dialClass discrimination, metric label) — extend the "+
							"allowlist in tools/archtest/adapter_net_transient_"+
							"funnel_test.go AND ADR 202605161800 in the same PR",
						valueSpecFirstName(vs), p.Pkg.Name(), fn,
					),
				})
			})
		})
	}
	return ds
}

// scanHelperFormViolations enforces TRANSIENT-NET-HELPER-FORM-01 on the body
// of the funcName function. The canonical form is:
//
//	var n net.Error
//	return errors.As(err, &n)
//
// (with a leading `*url.Error` unwrap permitted, per the helper's documented
// shape — see pkg/errcode.IsTransientNet godoc).
//
// Three checks:
//
//  1. Positive shape — body MUST contain at least one CallExpr resolving to
//     stdlib errors.As whose second argument is `&<ident>` where the ident's
//     static type is the net.Error interface. Absence → RED (catches empty
//     stubs like `return false` / `return true` that would silently disable
//     the helper).
//  2. Timeout()-filter denylist — `.Timeout()` SelectorExpr resolving to the
//     net.Error method is RED (would narrow transient classification).
//  3. net.* concrete-subtype narrowing denylist — `*net.X` StarExpr where X
//     is not the Error interface is RED (e.g. *net.OpError / *net.DNSError;
//     classification must key on the interface, not a specific subtype).
func scanHelperFormViolations(p *Pass, funcName string) []Diagnostic {
	var ds []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Name == nil || fd.Name.Name != funcName || fd.Body == nil {
				return
			}
			ds = append(ds, scanHelperPositiveErrorsAs(p, file, fd, funcName)...)
			EachInSubtree[ast.SelectorExpr](fd.Body, func(sel *ast.SelectorExpr) {
				if sel.Sel == nil {
					return
				}
				if sel.Sel.Name == "Timeout" && isNetErrorMethod(p.TypesInfo, sel) {
					ds = append(ds, Diagnostic{
						Rel:  p.Rel(file),
						Line: p.Fset.Position(sel.Pos()).Line,
						Message: fmt.Sprintf(
							"TRANSIENT-NET-HELPER-FORM-01: %s body must not "+
								"contain net.Error.Timeout() filtering — the "+
								"helper widens transient classification to any "+
								"net.Error (dial refused / reset / DNS included)",
							funcName,
						),
					})
				}
			})
			EachInSubtree[ast.StarExpr](fd.Body, func(se *ast.StarExpr) {
				if isNetSubtype(p.TypesInfo, se.X) {
					ds = append(ds, Diagnostic{
						Rel:  p.Rel(file),
						Line: p.Fset.Position(se.Pos()).Line,
						Message: fmt.Sprintf(
							"TRANSIENT-NET-HELPER-FORM-01: %s body must not "+
								"narrow to a specific net.* subtype (*net.OpError / "+
								"*net.DNSError) — classification keys on net.Error "+
								"interface membership alone",
							funcName,
						),
					})
				}
			})
		})
	}
	return ds
}

// scanHelperPositiveErrorsAs implements TRANSIENT-NET-HELPER-FORM-01 positive
// shape check (#1 in scanHelperFormViolations docs): the body must contain
// at least one stdlib errors.As CallExpr whose second argument is `&<ident>`
// with the ident's static type resolving to the net.Error interface.
//
// Returns a diagnostic if absent. An empty body, `return false`, or any body
// missing this canonical call site fails the check — closing the gap where
// negative-only scans would silently accept a stub that disables transient
// classification.
func scanHelperPositiveErrorsAs(p *Pass, file *ast.File, fd *ast.FuncDecl, funcName string) []Diagnostic {
	_, found := FindFirstInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) bool {
		if !isErrorsAsCall(p.TypesInfo, call) || len(call.Args) < 2 {
			return false
		}
		return isAddressOfNetErrorIdent(p.TypesInfo, call.Args[1])
	})
	if found {
		return nil
	}
	return []Diagnostic{{
		Rel:  p.Rel(file),
		Line: p.Fset.Position(fd.Pos()).Line,
		Message: fmt.Sprintf(
			"TRANSIENT-NET-HELPER-FORM-01: %s body must contain a canonical "+
				"`errors.As(err, &netErrVar)` call where netErrVar is typed "+
				"net.Error — empty / stub bodies (return false / return true) "+
				"are RED (positive shape lock; closes the negative-only-scan gap)",
			funcName,
		),
	}}
}

// scanErrorsAsNetSubtypeNarrow reports every `errors.As(err, &op)` CallExpr
// in production code whose enclosing function is OUTSIDE the allowlist AND
// whose second argument has a static type of `**net.X` (i.e. `&op` where
// `op : *net.X` and X is a concrete net.* subtype like OpError / DNSError /
// AddrError). This catches the narrowing-bypass form:
//
//	var op *net.OpError
//	if errors.As(err, &op) { ... transient-decision ... }
//
// — equivalent to declaring `var n net.Error` (which the existing detector
// flags) but using a concrete subtype to slip past the
// `var x net.Error`-only check. Locking this form makes
// ADAPTER-NET-TRANSIENT-FUNNEL-01 complete in the downstream direction.
func scanErrorsAsNetSubtypeNarrow(p *Pass, allowlist map[string]map[string]struct{}) []Diagnostic {
	allowed, tracked := allowlist[p.Pkg.Path()]
	if !tracked {
		if !inEnforcementScope(p.Pkg.Path()) {
			return nil
		}
		allowed = map[string]struct{}{}
	}
	var ds []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if !isErrorsAsCall(p.TypesInfo, call) {
				return
			}
			if len(call.Args) < 2 {
				return
			}
			subtype, isNarrow := addressOfNetSubtypeName(p.TypesInfo, call.Args[1])
			if !isNarrow {
				return
			}
			fn := enclosingFuncName(file, call.Pos())
			if fn == "" {
				fn = "<package scope>"
			}
			if _, ok := allowed[fn]; ok {
				return
			}
			ds = append(ds, Diagnostic{
				Rel:  p.Rel(file),
				Line: p.Fset.Position(call.Pos()).Line,
				Message: fmt.Sprintf(
					"`errors.As(err, &<*net.%s>)` narrowing form in %s.%s is "+
						"outside the ADAPTER-NET-TRANSIENT-FUNNEL-01 allowlist; "+
						"declaring `var x *net.%s; errors.As(err, &x)` for a "+
						"transient/permanent decision is equivalent to "+
						"`var n net.Error; errors.As(err, &n)` with a concrete "+
						"subtype — route the decision through "+
						"errcode.IsTransientNet, or extend the allowlist + ADR "+
						"202605161800 in the same PR",
					subtype, p.Pkg.Name(), fn, subtype,
				),
			})
		})
	}
	return ds
}

// isErrorsAsCall reports whether call's callee resolves (via *types.Info) to
// stdlib errors.As. Robust to import aliases (`stderrors "errors"`) and dot
// imports.
func isErrorsAsCall(info *types.Info, call *ast.CallExpr) bool {
	pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
	if !ok {
		return false
	}
	return pkgPath == "errors" && name == "As"
}

// isAddressOfNetErrorIdent reports whether expr is `&<ident>` and ident's
// static type is the net.Error interface (after types.Unalias).
func isAddressOfNetErrorIdent(info *types.Info, expr ast.Expr) bool {
	unary, ok := expr.(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return false
	}
	return isExprStaticallyNetError(info, unary.X)
}

// isExprStaticallyNetError reports whether expr's static type resolves to
// the net.Error interface. Walks through types.Unalias to handle
// `type myNetErr = net.Error` alias declarations.
func isExprStaticallyNetError(info *types.Info, expr ast.Expr) bool {
	t := info.TypeOf(expr)
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	if named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "net" && named.Obj().Name() == "Error"
}

// addressOfNetSubtypeName reports whether expr is `&<ident>` where ident's
// static type is `*net.X` for some concrete X != "Error". On match, returns
// the subtype name (e.g. "OpError" / "DNSError" / "AddrError"). Alias-aware
// via types.Unalias.
func addressOfNetSubtypeName(info *types.Info, expr ast.Expr) (string, bool) {
	unary, ok := expr.(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return "", false
	}
	t := info.TypeOf(unary.X)
	if t == nil {
		return "", false
	}
	t = types.Unalias(t)
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return "", false
	}
	elem := types.Unalias(ptr.Elem())
	named, ok := elem.(*types.Named)
	if !ok {
		return "", false
	}
	if named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", false
	}
	if named.Obj().Pkg().Path() != "net" {
		return "", false
	}
	if named.Obj().Name() == "Error" {
		return "", false
	}
	return named.Obj().Name(), true
}

// isNetErrorTypeExpr reports whether typeExpr resolves to the stdlib
// net.Error interface type. Uses types.Unalias to handle the
// `type myNetErr = net.Error` alias declaration form (Go 1.22+ default;
// without Unalias, `t.(*types.Named)` may fail for *types.Alias values).
func isNetErrorTypeExpr(info *types.Info, typeExpr ast.Expr) bool {
	t := info.TypeOf(typeExpr)
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	if named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "net" && named.Obj().Name() == "Error"
}

// isNetErrorMethod reports whether selectorExpr.Sel is a method declared on
// the net.Error interface (Timeout / Error). Defensive: matches by receiver
// type name + package path "net".
func isNetErrorMethod(info *types.Info, sel *ast.SelectorExpr) bool {
	t := info.TypeOf(sel.X)
	if t == nil {
		return false
	}
	// Walk through pointer / named to underlying interface.
	for {
		switch tt := t.(type) {
		case *types.Pointer:
			t = tt.Elem()
		case *types.Named:
			if tt.Obj() != nil && tt.Obj().Pkg() != nil &&
				tt.Obj().Pkg().Path() == "net" && tt.Obj().Name() == "Error" {
				return true
			}
			t = tt.Underlying()
		default:
			// Interface type with no name (zero value var of named interface
			// resolves to *types.Named above; *types.Interface fallthrough is
			// the assigned-into typed-error case which we treat conservatively).
			if _, ok := t.(*types.Interface); ok {
				return true
			}
			return false
		}
	}
}

// isNetSubtype reports whether typeExpr names a concrete net.* type (OpError,
// DNSError, AddrError, etc.) — anything in the "net" package other than the
// Error interface itself.
func isNetSubtype(info *types.Info, typeExpr ast.Expr) bool {
	t := info.TypeOf(typeExpr)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	if named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "net" && named.Obj().Name() != "Error"
}

// inEnforcementScope reports whether pkgPath is in the enforcement domain
// (any adapter package + pkg/errcode). Untracked packages outside this scope
// (e.g., kernel, runtime) are silently exempt.
//
// kernel/, runtime/, cells/ packages are exempt: they do not perform
// adapter-level transient classification; introducing net.Error transient
// gating there is explicitly out of scope of this rule. pkg/errcode is
// enforced via the allowlist (helper site).
func inEnforcementScope(pkgPath string) bool {
	for prefix := range netErrorAllowlist {
		// "/adapters/..." and "/pkg/errcode" — match by suffix-rooted prefix.
		if hasPathSuffix(pkgPath, prefix) {
			return true
		}
	}
	// Also enforce adapters/* even for non-allowlisted adapter packages
	// (e.g. a new adapters/kafka). The module path prefix is module-root +
	// "/adapters/" — testing by substring keeps the rule module-agnostic.
	const adaptersSeg = "/adapters/"
	return containsSegment(pkgPath, adaptersSeg)
}

func hasPathSuffix(full, suffix string) bool {
	if len(full) < len(suffix) {
		return false
	}
	return full[len(full)-len(suffix):] == suffix
}

func containsSegment(s, seg string) bool {
	if len(s) < len(seg) {
		return false
	}
	for i := 0; i+len(seg) <= len(s); i++ {
		if s[i:i+len(seg)] == seg {
			return true
		}
	}
	return false
}

// valueSpecFirstName returns the first declared identifier in a `var x[, y, ...]`
// spec. Empty if Names is nil — defensive against malformed AST.
func valueSpecFirstName(vs *ast.ValueSpec) string {
	if len(vs.Names) == 0 || vs.Names[0] == nil {
		return "_"
	}
	return vs.Names[0].Name
}
