// INVARIANT: READYZ-PROBE-NAMING-01
//
// readyz_probe_naming_test.go — READYZ-PROBE-NAMING-01
//
// Rule: every probe constructed via kernel/healthz.NewProbe(name, fn) whose
// name resolves to a compile-time constant string must use snake_case — no
// hyphens. Dependency-availability probes additionally end with the _ready
// suffix by convention (e.g. postgres_ready, accesscore_repo_ready). Probe
// names are stable operability contracts surfaced in /readyz verbose output
// and consumed by dashboards/alerts; a hyphen drift silently breaks them.
//
// Tool: RunTypedProduction (040 Pass-Driver). The callee is resolved via
// *types.Info.Uses to kernel/healthz.NewProbe (both the cross-package selector
// form healthz.NewProbe and the same-package bare-ident form inside
// kernel/healthz's own files). The name argument is then folded via
// typeseval.EvaluateConstString, which accepts BasicLit / const-bound Ident
// (e.g. the cellgen-emitted ProbeRepoReady = "configcore_repo_ready") /
// SelectorExpr-to-const / BinaryExpr-of-consts. A hyphen in the folded value
// is a violation.
//
// This replaces the pre-PR-886 scan of the deleted reg.Health(name, fn)
// registration method; healthz.NewProbe is now the canonical typed constructor
// that carries a probe name.
//
// Module-wide (not cells/+adapters/ scoped) because NewProbe is now the single
// named-probe constructor across kernel framework probes (config_watcher,
// config_drift), cellgen repo probes, and adapters — one funnel, one scan.
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. Non-constant probe names — e.g. kernel/outbox.emitter.go builds
//     healthz.NewProbe("outbox-failopen-rate."+e.cellID, …). EvaluateConstString
//     returns false on the BinaryExpr (cellID is runtime), so it is skipped.
//     The "outbox-failopen-rate" hyphen there is intentional and documented at
//     the call site; statically validating a runtime-composed name is out of
//     scope. Compensation: the constant prefix is reviewed at the call site.
//  2. Adapter checker-name shapes that do NOT go through healthz.NewProbe —
//     adapterutil.HealthToCheckers(name, …) arguments, map-literal keys
//     ("postgres_ready": fn), and consts like ReadyProbeName="s3_ready". These
//     are a separate naming surface; all current values are snake_case+_ready
//     by inspection. Compensation: their names flow into the aggregator via
//     WithHealthChecker and are reviewed at the adapter; a future widening to
//     scan HealthToCheckers args is tracked if drift appears.
//
// Reverse self-check:
//   - TestReadyzProbeNaming_RedHyphen — fixture package (build tag
//     archtest_fixture) calls healthz.NewProbe with a hyphenated constant name;
//     the rule MUST flag it. Proves both the callee resolution and the
//     EvaluateConstString-then-hyphen detection.
//
// ref: .claude/rules/gocell/observability.md — Readyz Probe 命名
// ref: kernel/healthz/probe.go — NewProbe canonical constructor
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/require"
)

const readyzProbeNaming01 = "READYZ-PROBE-NAMING-01"

const healthzPkgPath = "github.com/ghbvf/gocell/kernel/healthz"

// TestReadyzProbeNaming scans production code for healthz.NewProbe calls whose
// constant name argument contains a hyphen.
func TestReadyzProbeNaming(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		return scanProbeNameViolations(p)
	})

	Report(t, readyzProbeNaming01, diags)
}

// TestReadyzProbeNaming_RedHyphen is the reverse self-check: a fixture calls
// healthz.NewProbe with a hyphenated constant probe name. The rule MUST flag it.
func TestReadyzProbeNaming_RedHyphen(t *testing.T) {
	t.Parallel()

	fixturePattern := "./tools/archtest/readyz_probe_naming_fixtures/red_hyphen/..."
	diags := RunTypedFixture(t, FixtureOpts{},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			return scanProbeNameViolations(p)
		},
	)

	require.NotEmpty(t, diags,
		"READYZ-PROBE-NAMING-01 reverse self-check: fixture must produce ≥1 violation "+
			"(fixture calls healthz.NewProbe with a hyphenated constant probe name)")
}

// scanProbeNameViolations scans p.Files for healthz.NewProbe calls whose first
// argument folds to a constant string containing a hyphen.
func scanProbeNameViolations(p *Pass) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if len(call.Args) < 1 {
				return
			}
			if !isHealthzNewProbeCall(call.Fun, p.TypesInfo) {
				return
			}
			name, ok := EvaluateConstString(p.TypesInfo, call.Args[0])
			if !ok {
				return // non-constant name — see blind spot 1
			}
			if !containsHyphen(name) {
				return
			}
			pos := p.Fset.Position(call.Args[0].Pos())
			diags = append(diags, Diagnostic{
				Rel:  p.Rel(file),
				Line: pos.Line,
				Message: fmt.Sprintf("READYZ-PROBE-NAMING-01: healthz.NewProbe name %q contains a hyphen — "+
					"probe names are snake_case ops contracts (dependency probes end with _ready, e.g. postgres_ready)", name),
			})
		})
	}
	return diags
}

func containsHyphen(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			return true
		}
	}
	return false
}

// isHealthzNewProbeCall reports whether funExpr resolves (via *types.Info) to
// kernel/healthz.NewProbe, accepting both the cross-package selector form
// (healthz.NewProbe) and the same-package bare-ident form (NewProbe inside the
// kernel/healthz package's own files).
func isHealthzNewProbeCall(funExpr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	var ident *ast.Ident
	switch fn := funExpr.(type) {
	case *ast.SelectorExpr:
		if fn.Sel == nil || fn.Sel.Name != "NewProbe" {
			return false
		}
		ident = fn.Sel
	case *ast.Ident:
		if fn.Name != "NewProbe" {
			return false
		}
		ident = fn
	default:
		return false
	}
	fn, ok := info.Uses[ident].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == healthzPkgPath && fn.Name() == "NewProbe"
}
