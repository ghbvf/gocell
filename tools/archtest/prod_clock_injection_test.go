// PROD-CLOCK-INJECTION-01 — invariant-driven gate for *production* code.
//
// Invariant: In every Go file whose role is "production code"
// (tools/internal/fileroles.IsProductionCode), there must be no direct call
// to time.Now(), time.Since(...), time.Until(...), or time.NewTimer(...).
// All wall-clock interactions must flow through an injected
// kernel/clock.Clock — production code must never read the wall clock
// directly.
//
// The single whitelist is the kernel/clock package itself, where the
// canonical Real() implementation legitimately delegates to the standard
// library. Every other package must thread a clock.Clock through its
// constructor and call clk.Now() / clk.Since() / clk.Until() /
// clk.NewTimerAt() instead.
//
// This gate is the static enforcement of the design captured in
// docs/architecture/adr/202605021400-D6-kernel-clock-injection.md and is
// the structural complement to G6's TEST-SLEEP-DISCIPLINE-01: G6 made the
// existing wall-clock dependencies in test code visible; this gate prevents
// new ones from sneaking back into production code.
//
// Companion gates:
//   - TEST-SLEEP-DISCIPLINE-01 catalogs unavoidable test sleeps with
//     justifications.
//   - TEST-TIME-LITERAL-01 forces test Duration values into named consts.
//
// Platform scope: Linux CI (same as TEST-TIME-LITERAL-01).
//
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
package archtest

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// allowedRealClockPaths lists the paths whose Go files may legitimately
// call into the stdlib time package directly:
//
//   - kernel/clock owns the Real implementation; kernel/clock/clockmock
//     owns the deterministic test fake. Both are the boundary between
//     "framework infrastructure that talks to the wall clock" and "every
//     other package that must not".
//
//   - pkg/ packages are constrained by the LAYER-* depguard rules to
//     stdlib-only imports, so they cannot reach kernel/clock. The fileroles
//     IsProductionCode check still keeps them in scope for other archtest
//     gates, but each pkg/ wall-clock dependency must be a thin local
//     Clock interface (a single Now() method) that downstream callers
//     satisfy by passing their injected kernel/clock.Clock — which is
//     structurally compatible. The wall-clock entry point in the local
//     realClock fallback is therefore exempt here, just like kernel/clock
//     itself.
var allowedRealClockPaths = []string{
	"kernel/clock/",
	"pkg/securecookie/",
}

// forbiddenTimeFns lists the functions on `time` that production code
// must not call directly. Each entry maps to the matching Clock interface
// method that callers must use instead.
var forbiddenTimeFns = map[string]string{
	"Now":      "clock.Clock.Now",
	"Since":    "clock.Clock.Since",
	"Until":    "clock.Clock.Until",
	"NewTimer": "clock.Clock.NewTimerAt",
}

// TestProdClockInjection enforces PROD-CLOCK-INJECTION-01: production code
// (anything classified by fileroles.IsProductionCode) must not call
// time.Now / time.Since / time.Until / time.NewTimer directly. The
// single whitelist is the kernel/clock package itself.
//
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
func TestProdClockInjection(t *testing.T) {
	t.Parallel()
	// Intentionally not honoring testing.Short — see TestTestTimeLiteralConst.

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackages(root, true, testTimeLiteralBuildTags, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			rel, ok := fileroles.Rel(root, abs)
			if !ok || !fileroles.IsProductionCode(rel) {
				continue
			}
			if isAllowedRealClockPath(rel) {
				continue
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := matchedTimeFn(call)
				if !ok {
					return true
				}
				violations = append(violations, fmt.Sprintf(
					"%s:%d: time.%s — must use injected %s instead",
					rel, p.Fset.Position(call.Pos()).Line, name, forbiddenTimeFns[name],
				))
				return false
			})
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"PROD-CLOCK-INJECTION-01: production code must call clk.Now / "+
			"clk.Since / clk.Until / clk.NewTimerAt on an injected "+
			"kernel/clock.Clock; only kernel/clock itself may delegate "+
			"to the stdlib time package. ref: "+
			"docs/plans/202605011500-029-master-roadmap.md Track D #D6")
}

// isAllowedRealClockPath reports whether rel falls under one of the
// allowedRealClockPaths roots.
func isAllowedRealClockPath(rel string) bool {
	for _, prefix := range allowedRealClockPaths {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// matchedTimeFn reports whether call is one of the forbidden top-level
// time functions and returns the function name. Selector expressions on a
// local `time` identifier match (the codebase uniformly imports
// `"time"`); aliased imports such as `time2 "time"` would not be caught,
// but no such aliasing exists in this repo and a future PR introducing
// one would be flagged in review.
func matchedTimeFn(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	if ident.Name != "time" {
		return "", false
	}
	if _, listed := forbiddenTimeFns[sel.Sel.Name]; !listed {
		return "", false
	}
	return sel.Sel.Name, true
}
