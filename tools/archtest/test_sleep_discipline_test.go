// INVARIANT: TEST-SLEEP-DISCIPLINE-01
//
// TEST-SLEEP-DISCIPLINE-01 — invariant-driven gate for *test* code.
//
// Invariant: In every Go file whose role is "test code"
// (tools/internal/fileroles.IsTestCode), every `time.Sleep(...)` call
// expression must carry a same-line annotation of the form
//
//	time.Sleep(x) //archtest:allow:test-sleep <reason>
//
// where <reason> is non-empty and explains why the wait cannot be expressed
// as `require.Eventually` (or analogous polling). This forces a paper trail
// for every wall-clock dependency in the test suite — new lazy "sleep N then
// assert" sites cannot land without a reviewer reading and challenging the
// reason, and grep across the repository produces a complete inventory of
// real-clock dependencies for D6 PROD-CLOCK-INJECTION-01.
//
// Categories of legitimate reasons currently in use:
//   - "TTL physical expiry; backend has no notification API"
//   - "OS signal handler install has no sync hook"
//   - "wait for goroutine to enter blocking call; no started observable"
//   - "debounce/coalesce window IS the test subject"
//   - "negative test: must elapse without state change"
//   - "sleep IS the fixture input under test"
//
// Companion gates:
//   - TEST-TIME-LITERAL-01 enforces named-constant-only Duration values.
//     This gate adds the structural requirement: even with named constants,
//     a sleep must justify itself.
//   - PROD-CLOCK-INJECTION-01 (Track D #D6, future) eliminates real-clock
//     dependencies from production code. The annotation inventory this gate
//     produces is the input for that work.
//
// Platform scope: Linux CI (same as TEST-TIME-LITERAL-01).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 + Track D #D6
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const sleepAllowMarker = "//archtest:allow:test-sleep"

// TestTestSleepDiscipline enforces TEST-SLEEP-DISCIPLINE-01: every
// `time.Sleep(...)` in test code must carry a `//archtest:allow:test-sleep
// <reason>` annotation on the same source line.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 + Track D #D6
func TestTestSleepDiscipline(t *testing.T) {
	t.Parallel()
	// Intentionally not honoring testing.Short — see TestTestTimeLiteralConst.

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackages(root, true, typeseval.FlatNonDefaultTags(), patterns...)
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
			if !ok || !fileroles.IsTestCode(rel) {
				continue
			}

			violations = append(violations, scanTestSleepDiscipline(p.Fset, file, rel)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"TEST-SLEEP-DISCIPLINE-01: every time.Sleep in test code must justify "+
			"itself with `//archtest:allow:test-sleep <reason>` on the same line. "+
			"Prefer require.Eventually for state-polling waits. "+
			"ref: docs/plans/202605011500-029-master-roadmap.md G6")
}

// scanTestSleepDiscipline walks a parsed file's AST for `time.Sleep(...)`
// CallExpr nodes. Each call must be accompanied by a same-line allow
// comment with a non-empty reason; otherwise it is reported as a violation.
func scanTestSleepDiscipline(fset *token.FileSet, file *ast.File, rel string) []string {
	allowedLines := allowMarkerLines(fset, file)

	var violations []string
	scanner.EachNode[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isTimeSleepCall(call) {
			return
		}
		// Match against the line of the closing `)` so multi-line calls
		// can place the marker on the trailing-paren line. Common case
		// (single-line call) collapses to the call's start line.
		line := fset.Position(call.Rparen).Line
		if allowedLines[line] {
			return
		}
		violations = append(violations, fmt.Sprintf(
			"%s:%d: time.Sleep without %s <reason>",
			rel, fset.Position(call.Pos()).Line, sleepAllowMarker,
		))
	})
	return violations
}

// isTimeSleepCall reports whether call is `time.Sleep(...)`. The check is
// purely syntactic — packages with a local `time` identifier shadowed
// would still match, but the convention across this codebase is uniform
// `import "time"`, and a shadow rename would itself be flagged in review.
func isTimeSleepCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "time" && sel.Sel.Name == "Sleep"
}

// allowMarkerLines returns the set of source line numbers that contain at
// least one `//archtest:allow:test-sleep <reason>` comment with a non-empty
// reason. We consume the file's comment groups directly — same-line attach
// is determined by line equality with the time.Sleep call.
func allowMarkerLines(fset *token.FileSet, file *ast.File) map[int]bool {
	out := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			text := strings.TrimSpace(c.Text)
			if !strings.HasPrefix(text, sleepAllowMarker) {
				continue
			}
			// `//archtest:allow:test-sleep` alone (no reason) does not
			// satisfy the gate — the reason is the whole point.
			rest := strings.TrimSpace(strings.TrimPrefix(text, sleepAllowMarker))
			if rest == "" {
				continue
			}
			line := fset.Position(c.Slash).Line
			out[line] = true
		}
	}
	return out
}
