// TEST-TIME-LITERAL-01 — invariant-driven gate for *test* code.
//
// Invariant: In every Go file whose role is "test code" (every *_test.go,
// every test-helper conformance.go, and every file under a test-helper
// package such as **/locktest/, **/outboxtest/, **/storetest/, **/healthtest/),
// any expression whose static type is time.Duration and whose subtree
// contains a BasicLit must appear directly in the initializer of a
// package-level const declaration. All other positions (function-local
// var/const, CallExpr argument, struct-literal field, return, switch case,
// for initializer, closure interior, type-conversion interior) are
// violations and must be replaced by either:
//
//  1. a constant from pkg/testutil/testtime (preferred for cross-cutting
//     timeouts: EventuallyDefault, MediumPoll, SelectShutdown, etc.); or
//  2. a package-level const at the top of the test file (for site-specific
//     deadlines such as redisConformanceTTLBuffer = 5 * time.Millisecond).
//
// Exceptions:
//   - A BasicLit whose token value is "0" is not a violation (return 0 / var
//     x time.Duration = 0 is idiomatic zero-value usage).
//   - The archtest gate itself (tools/archtest/) is exempt: fixtures must be
//     allowed to embed violations, and the gate's internal helpers may use
//     literals to express intent.
//
// Companion gates:
//   - PROD-DURATION-CONST-01 enforces the same rule on production files
//     (every non-test file under cmd/, kernel/, runtime/, adapters/, etc.).
//     Together, the two gates leave no production-or-test code path where a
//     time.Duration literal can hide outside a package-level const.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 TEST-TIME-LITERAL-01
package archtest

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// testTimeLiteralBuildTags lists every build tag whose tagged test files must
// participate in the gate. New build tags introduced by future tests must be
// added here so the gate sees them; otherwise tagged test files with literal
// durations would silently bypass the invariant.
var testTimeLiteralBuildTags = []string{
	"e2e", "integration", "pg", "examples_smoke", "otelcollector",
}

// TestTestTimeLiteralConst enforces TEST-TIME-LITERAL-01 using the same
// universal AST walk as PROD-DURATION-CONST-01: for every declaration that
// is not a package-level const block, any expression whose static type is
// time.Duration and whose subtree contains a BasicLit is a violation.
//
// The only difference from PROD-DURATION-CONST-01 is the file filter: we
// include exactly the files PROD-DURATION-CONST-01 excludes as "test code".
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6
func TestTestTimeLiteralConst(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads test variant packages module-wide, ~10-15s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackages(root, true, testTimeLiteralBuildTags, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		// Tests=true returns three variants per package: the production
		// package, the in-package test variant (which has *_test.go in
		// GoFiles), and an external "_test" package. We only need to walk
		// each absolute file path once.
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			if !testTimeLiteralIncludeAbs(root, abs) {
				continue
			}
			rel, _ := filepath.Rel(root, abs)
			rel = filepath.ToSlash(rel)

			violations = append(violations,
				scanProdDurationAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"TEST-TIME-LITERAL-01: extract test-time durations to a package-level const "+
			"(prefer pkg/testutil/testtime.* for cross-cutting timeouts; declare a "+
			"file-local package-level const for site-specific deadlines). "+
			"ref: docs/plans/202605011500-029-master-roadmap.md G6")
}

// testTimeLiteralIncludeAbs reports whether the absolute path is "test code"
// for the purposes of TEST-TIME-LITERAL-01. Test code is any of:
//
//   - *_test.go (the canonical Go test convention)
//   - **/conformance.go (driver-conformance suites that exercise an adapter
//     under test; every adapter has one and PROD-DURATION-CONST-01 excludes
//     them by name)
//   - any file under a test-helper package: locktest/, outboxtest/, storetest/,
//     healthtest/, or contracttest/ (these contain test fakes / drivers that
//     are imported only from *_test.go and never shipped)
//
// Excluded:
//   - tools/archtest/ (the gate itself, including fixtures and self-tests)
//   - vendor/, generated/, testdata/ (third-party / generated content)
//   - paths outside the module root
func testTimeLiteralIncludeAbs(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return false
	}
	// Hard exclusions take precedence: the gate must not flag its own
	// fixtures or any third-party / generated content. testdata/ matches
	// both top-level (`testdata/foo.go`) and nested (`pkg/x/testdata/foo.go`).
	switch {
	case strings.HasPrefix(rel, "tools/archtest/"):
		return false
	case strings.HasPrefix(rel, "vendor/"):
		return false
	case strings.HasPrefix(rel, "generated/"):
		return false
	case strings.HasPrefix(rel, "testdata/"), strings.Contains(rel, "/testdata/"):
		return false
	}
	// Inclusions: exactly the predicates PROD-DURATION-CONST-01 uses to
	// EXCLUDE test code, inverted.
	switch {
	case strings.HasSuffix(rel, "_test.go"):
		return true
	case strings.HasSuffix(rel, "/conformance.go"):
		return true
	case strings.Contains(rel, "/locktest/"):
		return true
	case strings.Contains(rel, "/outboxtest/"):
		return true
	case strings.Contains(rel, "/storetest/"):
		return true
	case strings.Contains(rel, "/healthtest/"):
		return true
	case strings.Contains(rel, "/contracttest/"):
		return true
	case strings.Contains(rel, "/commandtest/"):
		return true
	}
	return false
}
