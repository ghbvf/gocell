// INVARIANT: HEALTHZ-WRITE-01
//   - INVARIANT: HEALTHZ-TYPED-REGISTER-01
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
//   - A2 (downstream Hard): healthz.Aggregator.Register callsites are limited
//     to a closed set of caller pkg+file pairs (caller-identity allowlist):
//
//   - runtime/observability/healthz/aggregator.go (self-test fixture)
//
//   - runtime/observability/healthz/healthztest/conformance.go
//     (conformance harness — legitimately calls Register for test scenarios)
//
//   - runtime/bootstrap/phases_lifecycle.go (drainProbes)
//
//   - runtime/bootstrap/phases_events.go (registerHealthChecker)
//
//   - runtime/bootstrap/bootstrap_phases.go (registerHealthChecker helper)
//
//   - cells/<cell>/healthz_gen.go (cellgen typed helper RegisterRepoReady)
//     plus any test files (*_test.go).
//
//   - kernel/cell/healthz.go (cell.RegisterEmitterHealthProbes — the single
//     emitter-probe funnel shared by all cells). Any other Register callsite
//     fails CI.
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
//     violations of A1/A2/A3 each detected by the rule logic against a
//     RunTypedDir-loaded subpackage.
//
// HEALTHZ-TYPED-REGISTER-01
//
//	cells/ packages may NOT call Registry.Healthz() outside cellgen-generated
//	healthz_gen.go files. Detection:
//	  1. caller file basename must be "healthz_gen.go" for any call to
//	     the method Healthz on kernel/cell.Registrar receiver
//	  2. healthz_gen.go must contain the cellgen DO NOT EDIT marker line
//	This ensures hand-written cell code (cell_init.go, handler.go, etc.) routes
//	through the typed cellgen RegisterRepoReady helper (cell-repo probes) and the
//	kernel cell.RegisterEmitterHealthProbes funnel (emitter probes) — never
//	reg.Healthz() directly.
//
// # Tool blind spots (forms RunTyped / *types.Info cannot see)
//
// B1. Reflection-based aggregator access: reflect.ValueOf(reg).MethodByName("Healthz").
//
//	Call(...) would bypass HEALTHZ-TYPED-REGISTER-01. Reverse self-check
//	TestHealthzInvariants_ReverseBlindSpot_NoReflectHealthz confirms no cells/
//	production file uses MethodByName("Healthz").
//
// B2. Cross-package helper indirection (non-generated): a helper function in a
//
//	non-healthz_gen.go file wrapping reg.Healthz() would bypass
//	HEALTHZ-TYPED-REGISTER-01. Reverse self-check
//	TestHealthzInvariants_ReverseBlindSpot_NoLocalHealthzWrapper confirms no
//	cells/ non-test file defines a function whose name contains "Healthz" AND
//	calls reg.Healthz(), except in healthz_gen.go files.
//
// ref: kernel/healthz.Aggregator — probe registry interface
// ref: cells/<cell>/healthz_gen.go — cellgen typed helpers
// ref: .claude/rules/gocell/observability.md — Cell 级别 Repo Readiness Probe
// ref: HEALTHZ-HOLDER-SEAL-01 (gh #893) — closed won't-do; A3 holder axis is
// inexpressible in Go's type system (see A3 godoc above for full rationale)
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
	healthzAggPkgPath  = "github.com/ghbvf/gocell/kernel/healthz"
	healthzCellPkgPath = "github.com/ghbvf/gocell/kernel/cell"
)

// healthz path strings that A1 bans from being registered directly.
var healthzBannedPaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
}

// A2 allowlist: relative file paths (from module root) that are permitted to
// call healthz.Aggregator.Register. Any caller not in this set and not a test
// file fails CI.
//
// The allowlist is split into exact path matches and basename matches.
var healthzRegisterExactPaths = map[string]bool{
	// aggregator.go: production impl that defines Register; the entry is
	// defensive — Register is not called recursively today but the allowlist
	// guards future internal use (e.g. registering a self-test probe at
	// construction) from tripping A2.
	"runtime/observability/healthz/aggregator.go": true,
	// healthztest subpackage: conformance harness legitimately calls Register for test scenarios;
	// exact path covers non-_test.go file; _test.go suffix already covered by the test-file exemption.
	"runtime/observability/healthz/healthztest/conformance.go": true,
	"runtime/bootstrap/phases_lifecycle.go":                    true,
	"runtime/bootstrap/phases_events.go":                       true,
	"runtime/bootstrap/bootstrap_phases.go":                    true,
	// kernel/cell/healthz.go: the sanctioned emitter-probe funnel
	// cell.RegisterEmitterHealthProbes — the sole kernel/ caller of
	// healthz.Aggregator.Register. Cells route emitter probes through this
	// helper (the former per-cell cellgen RegisterEmitterProbes is removed);
	// cell-repo probes still go through cellgen RegisterRepoReady in
	// cells/<cell>/healthz_gen.go. This A2 caller-identity allowlist is a
	// downstream guard; the funnel's upstream type-system seal (the only Hard
	// upstream form) is infeasible, so HEALTHZ-HOLDER-SEAL-01 (gh issue #893) is
	// won't-do — see the A3 rationale in this file's package godoc.
	"kernel/cell/healthz.go": true,
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
		// First argument is the path pattern; Handle/HandleFunc both take path
		// as first arg for the http.Handle/HandleFunc form, or the pattern is
		// also the first arg for mux.Handle/mux.HandleFunc.
		if len(call.Args) == 0 {
			return
		}
		// Some mux signatures take pattern as arg[0], handler as arg[1].
		// net/http.Handle(pattern, handler) and net/http.HandleFunc(pattern, handler)
		// and mux.Handle(pattern, handler) all have pattern as first arg.
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

// ─── A2 detection ─────────────────────────────────────────────────────────────

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
	// fn.Pkg() is the package that declares Register on the Aggregator interface.
	return fn.Pkg() != nil && fn.Pkg().Path() == healthzAggPkgPath && fn.Name() == "Register"
}

// isCellgenHealthzGenPath reports whether rel has the exact path shape of a
// cellgen-generated healthz_gen.go, i.e. cells/<cell>/healthz_gen.go or
// examples/<demo>/cells/<cell>/healthz_gen.go. A bare basename match
// (filepath.Base == "healthz_gen.go") is intentionally NOT used: once A2's scan
// scope includes kernel/, a hand-written kernel/foo/healthz_gen.go (or any other
// non-cell path) would otherwise be allowlisted by filename alone, bypassing the
// Register callsite guard. Mirrors golangci-lint depguard's path-glob scoping
// rather than basename-wide exemption.
func isCellgenHealthzGenPath(rel string) bool {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) == 0 || segs[len(segs)-1] != "healthz_gen.go" {
		return false
	}
	switch {
	case len(segs) == 3 && segs[0] == "cells":
		return true // cells/<cell>/healthz_gen.go
	case len(segs) == 5 && segs[0] == "examples" && segs[2] == "cells":
		return true // examples/<demo>/cells/<cell>/healthz_gen.go
	default:
		return false
	}
}

// isAllowedA2Caller reports whether a Register call from the given file is in the
// allowlist. absPath is the on-disk path of rel; it is read to confirm the
// cellgen DO NOT EDIT marker for the healthz_gen.go funnel (a hand-written file
// at a valid path shape but without the marker is NOT exempt).
func isAllowedA2Caller(rel, absPath string) bool {
	slash := filepath.ToSlash(rel)
	// Exact path match (marker-independent: these are non-generated sanctioned callers).
	if healthzRegisterExactPaths[slash] {
		return true
	}
	// cellgen funnel: exact path shape AND the cellgen marker must both hold.
	if isCellgenHealthzGenPath(rel) && fileHasCellgenMarker(absPath) {
		return true
	}
	// Test files are always allowed
	if strings.HasSuffix(slash, "_test.go") {
		return true
	}
	return false
}

// scanHealthzA2 walks file for Aggregator.Register calls outside the allowlist.
func scanHealthzA2(fset *token.FileSet, file *ast.File, rel, absPath string, info *types.Info) []Diagnostic {
	if info == nil {
		return nil
	}
	if isAllowedA2Caller(rel, absPath) {
		return nil
	}
	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isAggregatorRegisterCall(call, info) {
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
					"healthz.Aggregator.Register called from non-allowlisted file %s:%d; "+
						"allowed callers: runtime/bootstrap/{phases_lifecycle,phases_events,bootstrap_phases}.go, "+
						"runtime/observability/healthz/{aggregator,healthztest/conformance}.go, cells/<cell>/healthz_gen.go, "+
						"kernel/cell/healthz.go, *_test.go "+
						"(HEALTHZ-WRITE-01/A2)",
					rel, line,
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

// ─── HEALTHZ-TYPED-REGISTER-01 detection ──────────────────────────────────────

// isRegistryHealthzCall reports whether call is a Healthz() method call on a
// kernel/cell.Registrar receiver.
func isRegistryHealthzCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Healthz" {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == healthzCellPkgPath && fn.Name() == "Healthz"
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

// scanHealthzTypedRegister01 walks file for reg.Healthz() calls outside
// cellgen-generated healthz_gen.go files in cells/.
func scanHealthzTypedRegister01(fset *token.FileSet, file *ast.File, rel string, info *types.Info, absPath string) []Diagnostic {
	if info == nil {
		return nil
	}
	// Only scan cells/ production files.
	if !strings.HasPrefix(filepath.ToSlash(rel), "cells/") {
		return nil
	}
	if strings.HasSuffix(rel, "_test.go") {
		return nil
	}
	base := filepath.Base(rel)

	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isRegistryHealthzCall(call, info) {
			return
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if seen[key] {
			return
		}
		seen[key] = true

		if base != "healthz_gen.go" {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"reg.Healthz() called from %s (basename %q) — only cellgen-generated "+
						"healthz_gen.go may call Healthz() directly; use the typed "+
						"cellgen RegisterRepoReady helper or kernel cell.RegisterEmitterHealthProbes "+
						"(HEALTHZ-TYPED-REGISTER-01)",
					rel, base,
				),
			})
			return
		}
		// basename is healthz_gen.go — verify the codegen marker is present.
		if !fileHasCellgenMarker(absPath) {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"healthz_gen.go at %s is missing the codegen marker line %q; "+
						"file must be generated by `gocell generate cell`, not hand-written "+
						"(HEALTHZ-TYPED-REGISTER-01)",
					rel, cellgenMarkerLine,
				),
			})
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── Production scan ──────────────────────────────────────────────────────────

// TestHealthzWrite01 enforces HEALTHZ-WRITE-01 sub-rules A1/A2/A3 across the
// production tree.
//
// # Blind spots (forms RunTyped cannot see)
//
// B-A1: A dynamic path string assembled at runtime (strings.Join, fmt.Sprintf)
// would bypass A1's EvaluateConstString resolution. Reverse self-check
// TestHealthzInvariants_ReverseBlindSpot_NoDynamicHealthzPath confirms no such
// construction exists.
//
// B-A2: A wrapper function that calls agg.Register indirectly and lives in a
// non-allowlisted file would bypass A2. Reverse self-check
// TestHealthzInvariants_ReverseBlindSpot_NoLocalRegisterWrapper confirms no
// non-allowlisted file defines a local function that calls Register on a
// healthz.Aggregator.
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

	var a1Diags, a2Diags, a3Diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()
			// A1/A2/A3 scan scope: kernel/ + cells/ + adapters/ + runtime/ + cmd/ + examples/.
			// kernel/ is in scope so the sanctioned emitter-probe funnel
			// kernel/cell.RegisterEmitterHealthProbes (the only kernel/ caller of
			// healthz.Aggregator.Register) is covered by the A2 caller allowlist —
			// closing the prior kernel/ A2 blind spot. A1 (no kernel /healthz HTTP
			// registration) and A3 (no kernel struct holds an Aggregator field —
			// Registrar.Healthz() is an interface method, not a struct field) stay clean.
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
				absPath := filepath.Join(root, rel)
				a1Diags = append(a1Diags, scanHealthzA1(p.Fset, f, rel, p.TypesInfo)...)
				a2Diags = append(a2Diags, scanHealthzA2(p.Fset, f, rel, absPath, p.TypesInfo)...)
				a3Diags = append(a3Diags, scanHealthzA3(p.Fset, f, rel, p.TypesInfo, p.Pkg)...)
			}
			return nil
		})

	Report(t, "HEALTHZ-WRITE-01/A1", a1Diags)
	Report(t, "HEALTHZ-WRITE-01/A2", a2Diags)
	Report(t, "HEALTHZ-WRITE-01/A3", a3Diags)
}

// TestHealthzTypedRegister01 enforces HEALTHZ-TYPED-REGISTER-01 across the
// production tree.
//
// # Blind spots (forms RunTyped cannot see)
//
// B1: Reflection MethodByName("Healthz") would bypass this rule.
// See TestHealthzInvariants_ReverseBlindSpot_NoReflectHealthz.
//
// B2: Cross-package indirection via a non-healthz_gen.go helper function.
// See TestHealthzInvariants_ReverseBlindSpot_NoLocalHealthzWrapper.
func TestHealthzTypedRegister01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	allPatterns := prodscan.PatternsExtended(root)

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if !strings.HasPrefix(p.Pkg.Path(), "github.com/ghbvf/gocell/cells/") {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				absPath := filepath.Join(root, rel)
				diags = append(diags, scanHealthzTypedRegister01(p.Fset, f, rel, p.TypesInfo, absPath)...)
			}
			return nil
		})

	Report(t, "HEALTHZ-TYPED-REGISTER-01", diags)
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

	var a1Diags, a2Diags, a3Diags, typedRegDiags []Diagnostic

	// Load the fixture module with RunTypedDir so it shares one type universe.
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				absPath := filepath.Join(fixtureDir, rel)

				a1Diags = append(a1Diags, scanHealthzA1(p.Fset, f, rel, p.TypesInfo)...)
				a2Diags = append(a2Diags, scanHealthzA2(p.Fset, f, rel, absPath, p.TypesInfo)...)
				a3Diags = append(a3Diags, scanHealthzA3(p.Fset, f, rel, p.TypesInfo, p.Pkg)...)
				typedRegDiags = append(typedRegDiags, scanHealthzTypedRegister01(p.Fset, f, rel, p.TypesInfo, absPath)...)
			}
			return nil
		})

	t.Run("A1_fires_on_direct_healthz_registration", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, a1Diags,
			"HEALTHZ-WRITE-01/A1 reverse fixture: expected ≥1 diagnostic for direct /healthz registration; "+
				"rule logic is broken or fixture does not contain the violation")
	})

	t.Run("A2_fires_on_non_allowlisted_register_call", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, a2Diags,
			"HEALTHZ-WRITE-01/A2 reverse fixture: expected ≥1 diagnostic for non-allowlisted Register callsite; "+
				"rule logic is broken or fixture does not contain the violation")
	})

	t.Run("A3_fires_on_rogue_aggregator_holder", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, a3Diags,
			"HEALTHZ-WRITE-01/A3 reverse fixture: expected ≥1 diagnostic for rogue Aggregator holder; "+
				"rule logic is broken or fixture does not contain the violation")
	})

	t.Run("TypedRegister01_fires_on_direct_healthz_call_in_cell", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, typedRegDiags,
			"HEALTHZ-TYPED-REGISTER-01 reverse fixture: expected ≥1 diagnostic for cells/ calling reg.Healthz() directly; "+
				"rule logic is broken or fixture does not contain the violation")
	})
}

// ─── Blind-spot reverse self-checks ──────────────────────────────────────────

// TestHealthzInvariants_ReverseBlindSpot_NoReflectHealthz (blind spot B1)
// asserts no cells/ production file uses reflect MethodByName("Healthz").
func TestHealthzInvariants_ReverseBlindSpot_NoReflectHealthz(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./cells/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, hit := range scanReflectStringArgCalls(p, f, reflectMethodByName,
					func(n string) bool { return n == "Healthz" }) {
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: hit.Line,
						Message: "reflect MethodByName(\"Healthz\") in cells/ bypasses " +
							"HEALTHZ-TYPED-REGISTER-01 (blind spot B1)",
					})
				}
			}
			return nil
		})

	assert.Empty(t, diags, "blind spot B1 self-check failed: reflect MethodByName(\"Healthz\") found in cells/")
}

// TestHealthzInvariants_ReverseBlindSpot_NoLocalHealthzWrapper (blind spot B2)
// asserts no cells/ non-test file defines a function whose name contains
// "Healthz" AND whose body calls reg.Healthz(), except in healthz_gen.go files.
func TestHealthzInvariants_ReverseBlindSpot_NoLocalHealthzWrapper(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./cells/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				// healthz_gen.go is the sanctioned location; skip it.
				if filepath.Base(rel) == "healthz_gen.go" {
					continue
				}
				EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if fn.Name == nil || !strings.Contains(fn.Name.Name, "Healthz") {
						return
					}
					if fn.Body == nil {
						return
					}
					EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok || sel.Sel.Name != "Healthz" {
							return
						}
						if !isRegistryHealthzCall(call, p.TypesInfo) {
							return
						}
						line := p.Fset.Position(call.Pos()).Line
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: line,
							Message: fmt.Sprintf(
								"function %q in %s calls reg.Healthz() outside healthz_gen.go — "+
									"use RegisterRepoReady / cell.RegisterEmitterHealthProbes (blind spot B2, "+
									"HEALTHZ-TYPED-REGISTER-01)",
								fn.Name.Name, rel,
							),
						})
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags, "blind spot B2 self-check failed")
}

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

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(findModuleRoot(t)),
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
				// Skip the sanctioned handler.
				if strings.HasSuffix(filepath.ToSlash(rel), healthzA1AllowedSuffix) {
					continue
				}
				// Look for fmt.Sprintf / strings.Join calls that include a healthz/readyz string literal.
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

// TestHealthzInvariants_ReverseBlindSpot_NoLocalRegisterWrapper (blind spot B-A2)
// asserts no non-allowlisted file defines a local function that calls Register
// on a healthz.Aggregator, which would hide the call from A2.
func TestHealthzInvariants_ReverseBlindSpot_NoLocalRegisterWrapper(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic

	root := findModuleRoot(t)
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()
			// kernel/ is in scope to mirror TestHealthzWrite01/A2 (which now scans
			// kernel/): a future un-allowlisted kernel/ Register-wrapper must also
			// be caught here. kernel/cell/healthz.go is skipped via isAllowedA2Caller.
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
				// Skip known-allowlisted files.
				if isAllowedA2Caller(rel, filepath.Join(root, rel)) {
					continue
				}
				// Look for functions containing "Register" in their name that also
				// call a method named "Register" (possible wrapper hiding).
				EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if fn.Name == nil || !strings.Contains(fn.Name.Name, "Register") {
						return
					}
					if fn.Body == nil {
						return
					}
					EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
						if !isAggregatorRegisterCall(call, p.TypesInfo) {
							return
						}
						line := p.Fset.Position(call.Pos()).Line
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: line,
							Message: fmt.Sprintf(
								"function %q at %s:%d wraps healthz.Aggregator.Register — "+
									"potential blind-spot B-A2 bypass (HEALTHZ-WRITE-01/A2)",
								fn.Name.Name, rel, line,
							),
						})
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags, "blind spot B-A2 self-check failed")
}

// TestHealthzInvariants_ReverseBlindSpot_NoBasenameBypass (blind spot B-A2b)
// guards the A2 allowlist against the basename-wide exemption that the kernel/
// scan-scope extension would otherwise re-open: a file named healthz_gen.go at a
// non-cell path, or a cell-path healthz_gen.go lacking the cellgen marker, must
// NOT be allowlisted. Exercises isAllowedA2Caller directly with synthetic
// rel/absPath pairs (cellgen marker presence is toggled via two temp files).
func TestHealthzInvariants_ReverseBlindSpot_NoBasenameBypass(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	withMarker := filepath.Join(dir, "withmarker.go")
	if err := os.WriteFile(withMarker, []byte(cellgenMarkerLine+"\npackage x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noMarker := filepath.Join(dir, "nomarker.go")
	if err := os.WriteFile(noMarker, []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		rel  string
		abs  string
		want bool
	}{
		{"cells cell healthz_gen with marker", "cells/accesscore/healthz_gen.go", withMarker, true},
		{"examples demo cell healthz_gen with marker", "examples/todoorder/cells/ordercell/healthz_gen.go", withMarker, true},
		{"kernel-path healthz_gen even with marker is NOT exempt", "kernel/foo/healthz_gen.go", withMarker, false},
		{"runtime-path healthz_gen is NOT exempt", "runtime/foo/healthz_gen.go", withMarker, false},
		{"cell-path healthz_gen WITHOUT marker is NOT exempt", "cells/foo/healthz_gen.go", noMarker, false},
		{"deeper cell-path healthz_gen is NOT exempt", "cells/foo/internal/healthz_gen.go", withMarker, false},
		{"non-gen cell file is NOT exempt", "cells/foo/cell_init.go", withMarker, false},
		{"exact-path kernel/cell/healthz.go is exempt (marker-independent)", "kernel/cell/healthz.go", noMarker, true},
		{"test file is exempt", "kernel/foo/something_test.go", noMarker, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isAllowedA2Caller(tt.rel, tt.abs); got != tt.want {
				t.Errorf("isAllowedA2Caller(%q) = %v, want %v (HEALTHZ-WRITE-01/A2 basename-bypass guard)",
					tt.rel, got, tt.want)
			}
		})
	}
}
