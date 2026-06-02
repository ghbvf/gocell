// INVARIANT: HEALTHZ-WRITE-01
//
// HEALTHZ-WRITE-01
//
//   - A1 (downstream Hard): production code in runtime/ + adapters/ + cells/
//     must not register /healthz or /readyz HTTP handlers directly — must go
//     through the healthz.Aggregator funnel exposed by runtime/http/health.Handler.
//     Detection: (http|mux).HandleFunc(_, lit, _) or (mux|http).Handle(lit, _)
//     where lit (resolved via EvaluateConstString) ∈ {"/healthz", "/readyz"}.
//     Allowlist: runtime/http/health/health.go.
//
//   - A2 (removed): healthz.Aggregator.Register caller allowlist was superseded
//     by PROBENAME-SEALED-FUNNEL-01/A3 once Registrar.Healthz() was retired.
//     reg.Healthz() no longer exists in the Registrar interface; any attempt
//     to call it is a compile error. Direct Aggregator.Register callsites are
//     now guarded by PROBENAME-SEALED-FUNNEL-01/A3.
//
//   - A3 (Medium archtest — strongest available Go form): structs holding
//     a healthz.Aggregator interface-typed field are restricted to:
//
//   - runtime/observability/healthz.aggregator (unexported, package-private)
//
//   - runtime/http/health.Handler (the HTTP transport)
//
//   - runtime/bootstrap.Bootstrap (the composition root)
//     Adding a holder elsewhere fails CI. A3 stays archtest — it is NOT
//     upgradable to a compile-time Hard seal, for two independent reasons:
//     (1) A3 restricts which structs may *hold* a field of type
//     healthz.Aggregator; Go has no mechanism to restrict who declares a field
//     of a given type. Interface sealing (an unexported marker method) restricts
//     *implementers*, not *holders* — a different axis entirely.
//     (2) Even the implementer axis is unsealable here: an unexported marker is
//     package-scoped to kernel/healthz, but Aggregator has cross-package
//     implementations (runtime/observability/healthz.aggregator,
//     kernel/cell.recorderProbeSink, the public healthztest.FakeAggregator, the
//     A4 violate fixture), and collapsing to a single in-package impl is blocked
//     by the kernel/healthz → kernel/outbox → kernel/healthz import cycle.
//     HEALTHZ-HOLDER-SEAL-01 (cap-13 §13.1, PR #886; gh issue #893) is therefore
//     closed won't-do; see ADR 202605041430-...-engineering-thinking.md
//     §"Amendment 2026-05-26".
//
//   - A4 reverse self-test fixture (testdata/healthz_violate/): synthetic
//     violations of A1/A3 each detected by the rule logic against a
//     Run(t, StandaloneModule(...))-loaded subpackage. (A2 violation fixture retained for A3
//     Register-from-non-allowlisted-file detection — the fixture structure
//     is unchanged, but HEALTHZ-TYPED-REGISTER-01 fixture leg is removed.)
//
// HEALTHZ-TYPED-REGISTER-01 has been retired: the rule scanned for
// cells/ calling reg.Healthz() directly. Since Registrar.Healthz() was
// removed from the Registrar interface as part of issue #1034
// (ProbeName sealed funnel), any remaining call is a compile error —
// archtest enforcement is structurally redundant. The funnel collapsed
// into PROBENAME-SEALED-FUNNEL-01 (probename_sealed_funnel_test.go).
//
// # Tool blind spots (forms a typed Run / *types.Info cannot see)
//
// B-A1: A dynamic path string assembled at runtime (strings.Join, fmt.Sprintf)
//
//	would bypass A1's EvaluateConstString resolution. Reverse self-check
//	TestHealthzInvariants_ReverseBlindSpot_NoDynamicHealthzPath confirms no such
//	construction exists.
//
// B-A3: A local var holding a healthz.Aggregator (not a struct field) would
//
//	bypass A3. A3 only covers struct field holders; local vars are not in scope
//	because A3's purpose is to prevent new types from silently owning the
//	aggregator reference — local variables in function bodies are transient and
//	do not constitute ownership.
//
// ref: kernel/healthz.Aggregator — probe registry interface
// ref: HEALTHZ-HOLDER-SEAL-01 (gh #893) — closed won't-do; A3 holder axis is
// inexpressible in Go's type system (see A3 godoc above for full rationale)
// ref: PROBENAME-SEALED-FUNNEL-01 (probename_sealed_funnel_test.go) — supersedes
// HEALTHZ-TYPED-REGISTER-01 + HEALTHZ-WRITE-01/A2 for probe registration funneling
package archtest

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	healthzAggPkgPath = "github.com/ghbvf/gocell/kernel/healthz"
)

// healthz path strings that A1 bans from being registered directly.
var healthzBannedPaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
}

// A3 allowlist: (pkg path, type name) pairs allowed to hold a healthz.Aggregator field.
var healthzHolderAllowlist = map[string]bool{
	"github.com/ghbvf/gocell/runtime/observability/healthz.aggregator": true, // unexported impl
	"github.com/ghbvf/gocell/runtime/http/health.Handler":              true, // HTTP transport
	"github.com/ghbvf/gocell/runtime/bootstrap.Bootstrap":              true, // composition root
}

// A1 allowlist: files whose path ends with this suffix are exempt from the
// direct /healthz or /readyz registration ban.
const healthzA1AllowedSuffix = "runtime/http/health/health.go"

// cellgenMarkerLine is the DO NOT EDIT marker that cellgen emits in healthz_gen.go.
const cellgenMarkerLine = "// Code generated by gocell generate cell. DO NOT EDIT."

// ─── A1 detection ─────────────────────────────────────────────────────────────

// isHTTPRegistrationCall reports whether call is one of:
//
//	http.HandleFunc(path, handler)
//	http.Handle(path, handler)
//	<mux>.HandleFunc(path, handler)
//	<mux>.Handle(path, handler)
//
// where the callee resolves to net/http.HandleFunc / net/http.Handle or a
// (*net/http.ServeMux) method of the same name.
func isHTTPRegistrationCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	name := sel.Sel.Name
	if name != "HandleFunc" && name != "Handle" {
		return false
	}
	// Check if it is a package-level call (http.Handle / http.HandleFunc)
	// or a method call on *http.ServeMux.
	switch x := sel.X.(type) {
	case *ast.Ident:
		// e.g. http.Handle(...)
		obj, ok := info.Uses[x]
		if !ok {
			return false
		}
		pkgName, ok := obj.(*types.PkgName)
		if !ok {
			return false
		}
		return pkgName.Imported().Path() == "net/http"
	default:
		// e.g. mux.Handle(...) — resolve via Selections
		fn, ok := ResolveMethodCall(info, sel)
		if !ok || fn == nil {
			return false
		}
		// Must be on *net/http.ServeMux
		recv := fn.Type().(*types.Signature).Recv()
		if recv == nil {
			return false
		}
		t := recv.Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok {
			return false
		}
		obj := named.Obj()
		return obj.Pkg() != nil && obj.Pkg().Path() == "net/http" && obj.Name() == "ServeMux"
	}
}

// scanHealthzA1 walks file for direct /healthz or /readyz HTTP handler
// registrations outside the sanctioned runtime/http/health/health.go file.
func scanHealthzA1(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	if info == nil {
		return nil
	}
	// Skip the sanctioned handler file.
	if strings.HasSuffix(filepath.ToSlash(rel), healthzA1AllowedSuffix) {
		return nil
	}
	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isHTTPRegistrationCall(call, info) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		pathArg := call.Args[0]
		resolved, ok := EvaluateConstString(info, pathArg)
		if !ok {
			return
		}
		if !healthzBannedPaths[resolved] {
			return
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if !seen[key] {
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"direct HTTP registration of %q detected at %s:%d; "+
						"use runtime/http/health.Handler (HEALTHZ-WRITE-01/A1)",
					resolved, rel, line,
				),
			})
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── A3 detection ─────────────────────────────────────────────────────────────

// fieldTypeIsAggregator reports whether fieldType AST node resolves to
// kernel/healthz.Aggregator (the interface). Uses types.Info to look up the
// type of the field.
func fieldTypeIsAggregator(fieldType ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[fieldType]
	if !ok {
		return false
	}
	t := tv.Type
	// May be a pointer to the interface (rare but possible).
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil &&
		obj.Pkg().Path() == healthzAggPkgPath &&
		obj.Name() == "Aggregator"
}

// scanHealthzA3 walks file for struct type declarations that hold a field of
// type healthz.Aggregator outside the allowlist.
func scanHealthzA3(fset *token.FileSet, file *ast.File, rel string, info *types.Info, pkg *types.Package) []Diagnostic {
	if info == nil || pkg == nil {
		return nil
	}
	var out []Diagnostic

	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil || ts.Name == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !fieldTypeIsAggregator(field.Type, info) {
				continue
			}
			holderKey := pkg.Path() + "." + ts.Name.Name
			if healthzHolderAllowlist[holderKey] {
				continue
			}
			pos := fset.Position(field.Pos())
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"struct %s in %s holds healthz.Aggregator field but is not in the "+
						"sanctioned holder allowlist "+
						"(runtime/observability/healthz.aggregator, runtime/http/health.Handler, "+
						"runtime/bootstrap.Bootstrap); "+
						"this allowlist is archtest-only — the holder axis is inexpressible "+
						"in Go's type system, so it cannot become a compile-time seal "+
						"(HEALTHZ-WRITE-01/A3 Medium)",
					ts.Name.Name, rel,
				),
			})
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── Aggregator.Register detection (retained for A3 fixture) ─────────────────

// isAggregatorRegisterCall reports whether call is a Register method call on a
// kernel/healthz.Aggregator receiver (via *types.Info.Selections).
func isAggregatorRegisterCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Register" {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == healthzAggPkgPath && fn.Name() == "Register"
}

// fileHasCellgenMarker reports whether the file at absPath contains the
// cellgen DO NOT EDIT marker line. absPath is constructed from
// archtest-internal module-root + relative path joins; G304 inclusion
// risk does not apply here.
func fileHasCellgenMarker(absPath string) bool {
	f, err := os.Open(absPath) //nolint:gosec // archtest-internal module-root relative path
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == cellgenMarkerLine {
			return true
		}
	}
	return false
}

// ─── Production scan ──────────────────────────────────────────────────────────

// TestHealthzWrite01 enforces HEALTHZ-WRITE-01 sub-rules A1/A3 across the
// production tree. A2 (Aggregator.Register caller allowlist) was retired when
// Registrar.Healthz() was removed — the call is now a compile error and
// archtest enforcement is structurally redundant. Direct Aggregator.Register
// callsites are now guarded by PROBENAME-SEALED-FUNNEL-01/A3.
//
// # Blind spots (forms a typed Run cannot see)
//
// B-A1: A dynamic path string assembled at runtime (strings.Join, fmt.Sprintf)
// would bypass A1's EvaluateConstString resolution. Reverse self-check
// TestHealthzInvariants_ReverseBlindSpot_NoDynamicHealthzPath confirms no such
// construction exists.
//
// B-A3: A local var holding a healthz.Aggregator (not a struct field) would
// bypass A3. A3 only covers struct field holders; local vars are not in scope
// because A3's purpose is to prevent new types from silently owning the
// aggregator reference — local variables in function bodies are transient and
// do not constitute ownership.
func TestHealthzWrite01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	allPatterns := prodscan.PatternsExtended(root)

	var a1Diags, a3Diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()

			if !strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/kernel/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/cells/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/adapters/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/runtime/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/cmd/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/examples/") {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a1Diags = append(a1Diags, scanHealthzA1(p.Fset, f, rel, p.TypesInfo)...)
				a3Diags = append(a3Diags, scanHealthzA3(p.Fset, f, rel, p.TypesInfo, p.Pkg)...)
			}
			return nil
		})

	Report(t, "HEALTHZ-WRITE-01/A1", a1Diags)
	Report(t, "HEALTHZ-WRITE-01/A3", a3Diags)
}

// ─── Reverse fixture self-tests ───────────────────────────────────────────────

// TestHealthzInvariants_ReverseFixture loads the synthetic violation fixture
// and asserts each rule fires on its corresponding violation.
//
// Each violation type must produce at least one diagnostic; if a rule fires
// zero diagnostics on its own synthetic violation, the test FAILS — meaning
// the detection logic is broken.
func TestHealthzInvariants_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "healthz_violate")

	var a1Diags, a3Diags []Diagnostic

	// Load the fixture module with Run(t, StandaloneModule(...)) so it shares one type universe.
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}

				a1Diags = append(a1Diags, scanHealthzA1(p.Fset, f, rel, p.TypesInfo)...)
				a3Diags = append(a3Diags, scanHealthzA3(p.Fset, f, rel, p.TypesInfo, p.Pkg)...)
			}
			return nil
		})

	t.Run("A1_fires_on_direct_healthz_registration", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, a1Diags,
			"HEALTHZ-WRITE-01/A1 reverse fixture: expected ≥1 diagnostic for direct /healthz registration; "+
				"rule logic is broken or fixture does not contain the violation")
	})

	t.Run("A3_fires_on_rogue_aggregator_holder", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, a3Diags,
			"HEALTHZ-WRITE-01/A3 reverse fixture: expected ≥1 diagnostic for rogue Aggregator holder; "+
				"rule logic is broken or fixture does not contain the violation")
	})
}

// ─── Blind-spot reverse self-checks ──────────────────────────────────────────

// TestHealthzInvariants_ReverseBlindSpot_NoDynamicHealthzPath (blind spot B-A1)
// asserts no production file assembles /healthz or /readyz via dynamic string
// construction (fmt.Sprintf / strings.Join) outside runtime/http/health/health.go.
func TestHealthzInvariants_ReverseBlindSpot_NoDynamicHealthzPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	bannedLiterals := []string{`"/healthz"`, `"/readyz"`, `"healthz"`, `"readyz"`}
	isBanned := func(s string) bool {
		for _, b := range bannedLiterals {
			if s == b {
				return true
			}
		}
		return false
	}

	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(findModuleRoot(t))),

		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()
			if !strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/cells/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/adapters/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/runtime/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/cmd/") &&
				!strings.HasPrefix(pkgPath, "github.com/ghbvf/gocell/examples/") {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}

				if strings.HasSuffix(filepath.ToSlash(rel), healthzA1AllowedSuffix) {
					continue
				}

				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					n := sel.Sel.Name
					if n != "Sprintf" && n != "Join" {
						return
					}
					EachInChildren[ast.BasicLit](call, func(lit *ast.BasicLit) {
						if lit.Kind != token.STRING {
							return
						}
						if isBanned(lit.Value) {
							line := p.Fset.Position(call.Pos()).Line
							diags = append(diags, Diagnostic{
								Rel:  rel,
								Line: line,
								Message: fmt.Sprintf(
									"dynamic construction containing healthz/readyz path via %s at %s:%d "+
										"— blind spot B-A1 detected (HEALTHZ-WRITE-01/A1)",
									n, rel, line,
								),
							})
						}
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags, "blind spot B-A1 self-check failed")
}
