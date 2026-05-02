// KERNEL-CLOCK-LEAF-FALLBACK-01 — invariant-driven gate forbidding
// leaf-level clock.Real() construction outside the composition root.
//
// Invariant: In every Go file whose role is "production code" (i.e.
// tools/internal/fileroles.IsProductionCode), there must be no direct call
// to kernel/clock.Real(). The single root Clock is constructed once at the
// composition root and threaded through every consumer; any leaf-level
// fallback ("if clk == nil { clk = clock.Real() }", "c := clock.Real(); for
// _, opt := range opts { opt(c) }", or struct-literal default) re-introduces
// the wall-clock surface that PROD-CLOCK-INJECTION-01 was meant to abstract
// over and reopens the door to "construct succeeds, first Now() panics"
// failure modes.
//
// The companion gate PROD-CLOCK-INJECTION-01 forbids calls to the standard
// library time package (time.Now, time.NewTimer, …). This gate sits one
// layer up: even when production code uses kernel/clock instead of stdlib
// time, it must obtain the Clock from a constructor parameter, not by
// calling kernel/clock.Real() itself.
//
// Whitelist (the only legitimate clock.Real() callers within the scanned
// surface):
//
//   - kernel/clock/clock.go              — Real() factory definition itself
//   - cmd/corebundle/                    — main composition root
//   - gocell.go                          — top-level entry
//   - tests/e2e/internal/clients/clients.go
//     — non-_test.go helper that owns the e2e suite's clock; the e2e suite
//     process treats this file as its composition root.
//
// Out-of-scope by fileroles (no allowlist entry needed):
//   - examples/ — every Go file under examples/ is excluded by
//     fileroles.IsProductionCode (PROD-DURATION-CONST-01 contract:
//     "example projects ship documentation, not production binaries").
//     Example main.go composition roots therefore do not appear in the
//     scanned surface and need no allowlist entry.
//   - *_test.go — fileroles.IsTestCode excludes them; gate scans
//     production code only. Test-side clock.Real() cleanup is tracked
//     separately as G12-TEST-CLOCK-REAL-CLEANUP.
//
// Resolution is type-driven: every *ast.SelectorExpr is run through
// go/types.Info.ObjectOf to obtain the resolved *types.Func, then gated on
// obj.Pkg().Path() == "github.com/ghbvf/gocell/kernel/clock" and
// obj.Name() == "Real". This makes the check immune to import aliases
// ("import c \"github.com/ghbvf/gocell/kernel/clock\"; c.Real()") and to
// dot-imports ("import . \"…/kernel/clock\"; Real()").
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: tools/archtest/prod_clock_injection_test.go — sibling type-aware gate
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
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

// kernelClockPkgPath is the import path of the package whose Real() factory
// the gate guards. Hard-coded so a rename of the clock package is loud
// (test breaks and forces explicit migration).
const kernelClockPkgPath = "github.com/ghbvf/gocell/kernel/clock"

// allowedRealCallerPaths lists the production-code paths that may call
// kernel/clock.Real() directly. See package doc for rationale.
//
// Each entry is matched as either an exact file path (when it ends in .go)
// or a directory prefix (otherwise). The exact-file form lets us exempt
// kernel/clock/clock.go (the Real() factory definition itself) without
// exempting kernel/clock/clockmock or any future sibling packages.
var allowedRealCallerPaths = []string{
	"kernel/clock/clock.go",                 // Real() factory definition
	"cmd/corebundle/",                       // main composition root
	"cmd/gocell/",                           // gocell CLI composition root
	"gocell.go",                             // top-level entry
	"tests/e2e/internal/clients/clients.go", // e2e suite composition root
	// examples/ is excluded by fileroles.IsProductionCode (see package doc),
	// so example composition roots (examples/iotdevice/main.go,
	// examples/ssobff/app.go, examples/todoorder/main.go) do not need
	// allowlist entries.
	//
	// Test-helper packages own clock.Real() construction so test callers
	// don't repeat it. They are imported only by *_test.go files; the
	// CLOCK-INJECTION-TEST-CALLSITE-01 archtest enforces that boundary.
	"cells/accesscore/internal/testutil/", // SessionRepoForTest / RealSessionRepo
}

// TestKernelClockLeafFallback enforces KERNEL-CLOCK-LEAF-FALLBACK-01
// against the production tree.
//
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6 closure
func TestKernelClockLeafFallback(t *testing.T) {
	t.Parallel()

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
			if isAllowedRealCallerPath(rel) {
				continue
			}

			violations = append(violations, scanLeafRealCallsAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"KERNEL-CLOCK-LEAF-FALLBACK-01: production code outside the composition "+
			"root must not call kernel/clock.Real(); receive a clock.Clock through "+
			"the constructor and validate at the boundary via clock.MustHaveClock. "+
			"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md")
}

// isAllowedRealCallerPath reports whether rel is exempt from the gate.
// Entries ending in .go match exactly; other entries match as directory
// prefixes.
func isAllowedRealCallerPath(rel string) bool {
	for _, allowed := range allowedRealCallerPaths {
		if strings.HasSuffix(allowed, ".go") {
			if rel == allowed {
				return true
			}
			continue
		}
		if strings.HasPrefix(rel, allowed) {
			return true
		}
	}
	return false
}

// scanLeafRealCallsAST walks file's AST and returns a sorted slice of
// violation strings ("rel:line: detail") for every call to
// kernel/clock.Real(). Detection is type-driven via info.ObjectOf so import
// aliases and dot-imports are uniformly covered.
func scanLeafRealCallsAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []string {
	var out []string
	seen := map[string]bool{}

	record := func(node ast.Node) {
		line := fset.Position(node.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, fmt.Sprintf(
			"%s:%d: kernel/clock.Real() — accept clock.Clock as a constructor "+
				"parameter and validate via clock.MustHaveClock", rel, line))
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.SelectorExpr:
			// Standard form: clock.Real / c.Real (alias).
			if matchedKernelClockReal(info, e.Sel) {
				record(e)
				return false
			}
		case *ast.Ident:
			// Dot-import form: `import . "…/kernel/clock"; Real()`.
			if matchedKernelClockReal(info, e) {
				record(e)
			}
		}
		return true
	})

	sort.Strings(out)
	return out
}

// matchedKernelClockReal reports whether ident resolves (via info.ObjectOf)
// to the package-level function kernel/clock.Real.
//
// Filters explicitly to *types.Func with a nil receiver so that references
// to a Real type / Real const / Real method on an unrelated package are
// not flagged.
func matchedKernelClockReal(info *types.Info, ident *ast.Ident) bool {
	if info == nil || ident == nil {
		return false
	}
	fn, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != kernelClockPkgPath {
		return false
	}
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return false
	}
	return fn.Name() == "Real"
}

// runLeafFallbackFixtureScan loads the fixture package at fixtureDir and
// returns the sorted slice of violation strings using the same predicate as
// TestKernelClockLeafFallback (scanLeafRealCallsAST). The whitelist is
// intentionally NOT applied here — every fixture path is treated as
// production code so the gate's detection logic is the only thing under test.
func runLeafFallbackFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackages(fixtureDir, false, nil, "./...")
	require.NoError(t, err, "packages.Load failed for fixture %s", fixtureDir)
	require.Empty(t, errs, "package load errors must fail-closed for %s: %v", fixtureDir, errs)

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

			rel, ok := fileroles.Rel(fixtureDir, abs)
			if !ok {
				continue
			}

			violations = append(violations,
				scanLeafRealCallsAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	return violations
}

// TestKernelClockLeafFallbackFixtures runs the KERNEL-CLOCK-LEAF-FALLBACK-01
// scanner over each fixture subpackage and asserts the expected violation
// count. Mirrors TestProdClockInjectionFixtures (sibling gate).
func TestKernelClockLeafFallbackFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/clock_leaf_fallback_fixtures"

	cases := []struct {
		pkg          string
		wantViolReps int // expected number of (file:line) violation reports
	}{
		// Positive — must produce 0 violations.
		{"compliant", 0},
		// Negative — exercises three call shapes (direct, alias, nil-fallback).
		// Each produces one (file:line) report (selector + alias + body all on
		// distinct lines), so 3 reports total.
		{"violates", 3},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			got := runLeafFallbackFixtureScan(t, base+"/"+tc.pkg)
			assert.Equal(t, tc.wantViolReps, len(got),
				"fixture %s: expected %d violation report(s), got %d: %v",
				tc.pkg, tc.wantViolReps, len(got), got)
		})
	}
}
