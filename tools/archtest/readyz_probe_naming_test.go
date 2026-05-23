// INVARIANT: READYZ-PROBE-NAMING-01
//
// readyz_probe_naming_test.go — READYZ-PROBE-NAMING-01
//
// Rule: every probe constructed via kernel/healthz.NewProbe(name, fn) must use
// snake_case — no hyphens. Dependency-availability probes additionally end with
// the _ready suffix by convention (e.g. postgres_ready, accesscore_repo_ready);
// framework probes (config_watcher, outbox_failopen_rate_<cell>) omit it. Probe
// names are stable operability contracts surfaced in /readyz verbose output and
// consumed by dashboards/alerts; a hyphen drift silently breaks them.
//
// Tool: RunTypedProduction (040 Pass-Driver). The callee is resolved via
// *types.Info.Uses to kernel/healthz.NewProbe (both the cross-package selector
// form healthz.NewProbe and the same-package bare-ident form inside
// kernel/healthz's own files). The name argument is checked two ways:
//   - const-foldable names (BasicLit, const-bound Ident such as the cellgen
//     ProbeRepoReady="configcore_repo_ready", SelectorExpr-to-const,
//     BinaryExpr-of-consts) are folded via typeseval.EvaluateConstString and
//     the whole value is hyphen-checked.
//   - composed names with a runtime segment (e.g. "outbox_failopen_rate_" +
//     e.cellID) are not const-foldable, so every string-literal operand of the
//     name expression is hyphen-checked — the literal prefix is still enforced.
//
// This replaces the pre-PR-886 scan of the deleted reg.Health(name, fn)
// registration method; healthz.NewProbe is now the canonical typed constructor
// that carries a probe name.
//
// Module-wide (not cells/+adapters/ scoped) because NewProbe is now the single
// named-probe constructor across kernel framework probes (config_watcher,
// config_drift, outbox_failopen_rate_<cell>), cellgen repo probes, and
// adapters — one funnel, one scan.
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. Fully runtime-computed names with NO string literal at all — e.g.
//     healthz.NewProbe(buildName(x), fn) where buildName returns a non-literal
//     string. There is no literal operand to hyphen-check. No such call site
//     exists in production (every probe name carries at least a literal prefix);
//     a future one would escape this rule. Compensation: NewProbe is the single
//     constructor, so any such site is grep-visible and reviewable.
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
//     archtest_fixture) calls healthz.NewProbe with a hyphenated constant name
//     AND a composed "prefix-bad_" + runtimeID name; the rule MUST flag both.
//     Proves callee resolution, the EvaluateConstString-then-hyphen path, and
//     the string-literal-operand path for composed names.
//
// ref: .claude/rules/gocell/observability.md — Readyz Probe 命名
// ref: kernel/healthz/probe.go — NewProbe canonical constructor
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
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

// scanProbeNameViolations scans p.Files for healthz.NewProbe calls whose probe
// name contains a hyphen — either as a folded compile-time constant, or as a
// string-literal operand of a composed (prefix + runtime segment) name.
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
			nameExpr := call.Args[0]
			// Const-foldable names are checked whole.
			if name, ok := EvaluateConstString(p.TypesInfo, nameExpr); ok {
				if strings.Contains(name, "-") {
					diags = append(diags, probeNameDiag(p, file, nameExpr, name))
				}
				return
			}
			// Composed names with a runtime segment (e.g. "prefix_" + cellID):
			// hyphen-check every string-literal operand so the literal prefix is
			// still enforced (see blind spot 1 for the no-literal case).
			EachInSubtree[ast.BasicLit](nameExpr, func(lit *ast.BasicLit) {
				if lit.Kind != token.STRING {
					return
				}
				val := strings.Trim(lit.Value, "`\"")
				if strings.Contains(val, "-") {
					diags = append(diags, probeNameDiag(p, file, lit, val))
				}
			})
		})
	}
	return diags
}

// probeNameDiag builds a READYZ-PROBE-NAMING-01 diagnostic for a hyphenated
// probe-name literal at node's position.
func probeNameDiag(p *Pass, file *ast.File, node ast.Node, name string) Diagnostic {
	pos := p.Fset.Position(node.Pos())
	return Diagnostic{
		Rel:  p.Rel(file),
		Line: pos.Line,
		Message: fmt.Sprintf("READYZ-PROBE-NAMING-01: healthz.NewProbe name literal %q contains a hyphen — "+
			"probe names are snake_case ops contracts (dependency probes end with _ready, e.g. postgres_ready)", name),
	}
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
