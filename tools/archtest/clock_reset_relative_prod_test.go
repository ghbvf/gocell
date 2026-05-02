// KERNEL-CLOCK-RESET-RELATIVE-PROD-01 — invariant-driven gate for *production* code.
//
// Invariant: In every Go file whose role is "production code"
// (tools/internal/fileroles.IsProductionCode), calls to the relative
// Timer.Reset(d time.Duration) method on values that implement
// kernel/clock.Timer are forbidden. Production code must use the absolute
// Timer.ResetAt(deadline time.Time) API to eliminate the read-then-act race
// between capturing a deadline and arming the timer.
//
// Detection is type-driven (go/types): the gate identifies any CallExpr of the
// form `<expr>.Reset(<arg>)` where:
//  1. The resolved *types.Func has name "Reset".
//  2. Its Signature has exactly one parameter of type time.Duration and
//     one result of type bool.
//  3. The receiver type (the type of <expr>) also exposes a method named
//     "ResetAt" — this is the structural marker that the receiver implements
//     something equivalent to kernel/clock.Timer (versus stdlib *time.Timer
//     or other Reset-carrying types such as Ticker, hash.Hash, etc.).
//
// Exemptions:
//   - _test.go files (test code may use relative Reset in fake-clock helpers).
//   - kernel/clock/clock.go (the interface definition itself; Reset is defined
//     there as part of the Timer interface and must remain for stdlib parity).
//   - kernel/clock/clockmock/ (the fake implementation's Reset method body).
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: tools/archtest/prod_clock_injection_test.go — companion gate
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

// clockResetRelativeExemptPaths lists path prefixes exempt from the gate.
// These packages define or implement the Reset method itself, not callers.
var clockResetRelativeExemptPaths = []string{
	"kernel/clock/clock.go",   // interface definition
	"kernel/clock/clockmock/", // fake implementation
}

// TestKernelClockResetRelativeProd enforces KERNEL-CLOCK-RESET-RELATIVE-PROD-01.
func TestKernelClockResetRelativeProd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackages(root, false, testTimeLiteralBuildTags, patterns...)
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
			if isClockResetRelativeExempt(rel) {
				continue
			}

			violations = append(violations, scanClockResetRelativeAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"KERNEL-CLOCK-RESET-RELATIVE-PROD-01: prod code must use ResetAt(deadline time.Time) "+
			"instead of Reset(d time.Duration) on clock.Timer values to avoid the read-then-act "+
			"race; ref: docs/architecture/202605021500-adr-kernel-clock-injection.md")
}

// isClockResetRelativeExempt reports whether rel is exempt from the gate.
func isClockResetRelativeExempt(rel string) bool {
	for _, prefix := range clockResetRelativeExemptPaths {
		if strings.HasPrefix(rel, prefix) || rel == prefix {
			return true
		}
	}
	return false
}

// scanClockResetRelativeAST walks file's AST and returns a sorted slice of
// violation strings for every call `<expr>.Reset(d)` where the receiver
// structurally implements kernel/clock.Timer (has both Reset(Duration)bool and
// ResetAt(Time)bool methods). This is the same predicate used by the fixture
// regression tests.
func scanClockResetRelativeAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []string {
	var out []string
	seen := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Reset" {
			return true
		}
		if info == nil {
			return true
		}
		fn, ok := info.ObjectOf(sel.Sel).(*types.Func)
		if !ok || fn == nil {
			return true
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			return true
		}
		// Must be a method (has receiver).
		if sig.Recv() == nil {
			return true
		}
		// Signature: Reset(time.Duration) bool
		if !isResetDurationBool(sig) {
			return true
		}
		// Receiver type must also expose ResetAt(time.Time) bool — the
		// structural marker for clock.Timer-like types.
		recvType := sig.Recv().Type()
		if !typeHasResetAt(recvType) {
			return true
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if !seen[key] {
			seen[key] = true
			out = append(out, fmt.Sprintf(
				"%s:%d: Timer.Reset(d time.Duration) — use ResetAt(deadline time.Time) instead "+
					"to avoid read-then-act race; "+
					"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md",
				rel, line))
		}
		return false
	})

	sort.Strings(out)
	return out
}

// isResetDurationBool reports whether sig matches Reset(time.Duration) bool.
func isResetDurationBool(sig *types.Signature) bool {
	if sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	param := sig.Params().At(0).Type()
	result := sig.Results().At(0).Type()
	if !isTimeDurationType(param) {
		return false
	}
	basic, ok := result.(*types.Basic)
	return ok && basic.Kind() == types.Bool
}

// typeHasResetAt reports whether t (or its underlying pointer/interface) exposes
// a method named "ResetAt" with signature ResetAt(time.Time) bool.
func typeHasResetAt(t types.Type) bool {
	mset := types.NewMethodSet(t)
	sel := mset.Lookup(nil, "ResetAt")
	if sel == nil {
		// Try pointer receiver too.
		mset = types.NewMethodSet(types.NewPointer(t))
		sel = mset.Lookup(nil, "ResetAt")
	}
	if sel == nil {
		return false
	}
	fn, ok := sel.Obj().(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	if sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	param := sig.Params().At(0).Type()
	result := sig.Results().At(0).Type()
	basic, ok := result.(*types.Basic)
	if !ok || basic.Kind() != types.Bool {
		return false
	}
	return isTimeTimeType(param)
}

// isTimeDurationType reports whether t is time.Duration.
func isTimeDurationType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "time" && obj.Name() == "Duration"
}

// isTimeTimeType reports whether t is time.Time.
func isTimeTimeType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "time" && obj.Name() == "Time"
}

// ---------------------------------------------------------------------------
// Fixture-based regression tests
// ---------------------------------------------------------------------------

// TestKernelClockResetRelativeFixtures verifies the scanner against the two
// fixture packages: one that violates the rule, one that is compliant.
func TestKernelClockResetRelativeFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := root + "/tools/archtest/testdata/clock_reset_relative_fixtures"

	cases := []struct {
		dir           string
		wantViolLines []int // nil = expect 0 violations
	}{
		{"compliant", nil},
		{"violates", []int{17}},
	}

	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := fixturesBase + "/" + tc.dir
			got := runClockResetRelativeFixtureScan(t, fixtureDir)

			if len(tc.wantViolLines) == 0 {
				assert.Empty(t, got, "fixture %s: expected 0 violations, got %v", tc.dir, got)
				return
			}

			assert.Equal(t, len(tc.wantViolLines), len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.dir, len(tc.wantViolLines), len(got), got)

			for i, line := range tc.wantViolLines {
				if i >= len(got) {
					break
				}
				prefix := fmt.Sprintf("usage.go:%d:", line)
				assert.Contains(t, got[i], prefix,
					"fixture %s violation[%d]: expected prefix %q, got %q",
					tc.dir, i, prefix, got[i])
			}
		})
	}
}

// runClockResetRelativeFixtureScan loads a standalone fixture module and runs
// the same scanner as TestKernelClockResetRelativeProd.
func runClockResetRelativeFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackages(fixtureDir, false, nil, "./...")
	require.NoError(t, err, "packages.Load failed for fixture %s", fixtureDir)
	require.Empty(t, errs, "package load errors for %s: %v", fixtureDir, errs)

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
				scanClockResetRelativeAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	return violations
}
