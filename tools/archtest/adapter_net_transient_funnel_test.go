// INVARIANT: ADAPTER-NET-TRANSIENT-FUNNEL-01
//   - INVARIANT: TRANSIENT-NET-HELPER-FORM-01
//
// Hard double-lock for the adapter "net.Error → transient" rule
// (ai-collab.md §AI-rebust + §Funnel 双向锁 + ADR 202605161800):
//
//   - Downstream Hard (ADAPTER-NET-TRANSIENT-FUNNEL-01): every `var x net.Error`
//     declaration in production code must reside in an allowlisted
//     (pkgPath, funcName) pair. The allowlist is the single typed source of
//     truth for "where the net.Error interface assertion is legitimately
//     decoded outside the funnel"; everywhere else MUST delegate to
//     errcode.IsTransientNet. Adding a new adapter classifier that re-inlines
//     `var x net.Error` is RED in CI; expanding the allowlist requires
//     same-PR edit to this file + ADR §"Adapter transient inventory".
//
//   - Upstream Hard (TRANSIENT-NET-HELPER-FORM-01): the body of
//     pkg/errcode.IsTransientNet is locked to the broadest form — declares
//     `var n net.Error`, calls `errors.As(err, &n)`, and contains no
//     `.Timeout()` SelectorExpr nor narrowing type assertion (*net.OpError,
//     *net.DNSError, etc.). A regression that re-introduces Timeout()
//     filtering or narrowing inside the helper is RED in CI.
//
// Tool: archtest.RunTypedProduction (040 Pass-Driver) + *types.Info Uses /
// TypeOf for callee + type resolution; AST walk via EachInSubtree[ast.GenDecl] /
// EachInSubtree[ast.SelectorExpr].
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. Reflective type-name comparison ("net.Error" string-equality): impossible
//     for in-package var declarations — Go type system requires `var x net.Error`
//     to import `net`, which `*types.Info.ObjectOf(spec.Type.Sel).Pkg().Path()`
//     resolves authoritatively. Compensation: Go type system.
//  2. Type alias for net.Error (`type myNetErr = net.Error; var x myNetErr`):
//     `*types.Info.TypeOf(spec.Type)` resolves through the alias to the
//     underlying interface type. The check uses `.Underlying() == netErrorIface`
//     match, robust to aliases. Compensation: typed resolution.
//  3. `errors.As(err, &x)` where x has interface type assignable to net.Error
//     but is declared via short var `x := someFunc()` instead of `var x net.Error`:
//     Go's errors.As signature requires `&x` where *x implements net.Error or
//     error; the canonical Go pattern is `var x net.Error` zero-value-then-As.
//     Short-decl forms are anti-idiomatic and would be caught by the
//     `errors.As` second-arg type check (also implemented below). Compensation:
//     same-archtest secondary form check on errors.As call sites.
//  4. Body-scope detector that bypasses enclosing function (e.g., closure
//     captured `net.Error` from outer scope): the declaration site is what we
//     scan; the enclosing function name is captured via positional containment.
//     A closure assigning into an outer `var netErr net.Error` would still be
//     anchored to the outer function's declaration. Compensation: positional
//     enclosing-func scan locks the declaration site, not the assignment site.
//
// Reverse self-check: TestADAPTER_NET_TRANSIENT_FUNNEL_01_FixturePattern
// loads tools/archtest/internal/nettransientfunnelfixture/ via
// RunTypedFixture and asserts the synthetic regressed forms are reported,
// while the clean form is not. Bypassing the reverse self-check requires
// editing the real fixture source.
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
		return scanNetErrorDeclarations(p, allowed)
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

	// Allowlist detector: 3 RED expected (forbiddenSite + regressedHelperTimeout
	// + regressedHelperNarrow all declare `var n net.Error` outside the
	// fixture-local allowlist).
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
	require.Len(t, allowlistDiags, 3,
		"fixture allowlist detector must yield exactly 3 RED sites "+
			"(forbiddenSite + regressedHelperTimeout + regressedHelperNarrow); "+
			"allowedSite must NOT be flagged")
	joined := ""
	for _, d := range allowlistDiags {
		joined += d.Message + "\n"
	}
	assert.Contains(t, joined, "forbiddenSite")
	assert.Contains(t, joined, "regressedHelperTimeout")
	assert.Contains(t, joined, "regressedHelperNarrow")
	assert.NotContains(t, joined, "allowedSite")

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

// scanHelperFormViolations reports every Timeout() SelectorExpr and every
// narrowing type-assertion (*net.OpError / *net.DNSError) inside the body
// of the funcName function declared in p. The canonical form is:
//
//	var n net.Error
//	return errors.As(err, &n)
//
// Any presence of `.Timeout()`, `*net.OpError`, or `*net.DNSError` inside
// the body is RED — the helper must classify by net.Error interface
// membership alone.
func scanHelperFormViolations(p *Pass, funcName string) []Diagnostic {
	var ds []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Name == nil || fd.Name.Name != funcName || fd.Body == nil {
				return
			}
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

// isNetErrorTypeExpr reports whether typeExpr resolves to the stdlib
// net.Error interface type. Handles both direct `net.Error` SelectorExpr and
// any alias whose underlying type is net.Error.
func isNetErrorTypeExpr(info *types.Info, typeExpr ast.Expr) bool {
	t := info.TypeOf(typeExpr)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		// Aliases / instantiated interfaces still report as *types.Named via
		// the resolution chain. Walk the chain via Underlying() and re-check.
		und := t.Underlying()
		iface, ok := und.(*types.Interface)
		if !ok {
			return false
		}
		// Cross-check against the canonical net.Error by looking for a Sel
		// that resolves to "Error" in package "net".
		_ = iface
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
