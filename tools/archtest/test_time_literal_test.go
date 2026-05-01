// TEST-TIME-LITERAL-01 — invariant-driven gate for *test* code.
//
// Invariant: In every Go file whose role is "test code" (see
// tools/internal/fileroles for the canonical classifier), any expression
// whose static type is time.Duration and whose subtree contains a BasicLit
// must appear directly in the initializer of a package-level const
// declaration. All other positions (function-local var/const, CallExpr
// argument, struct-literal field, return, switch case, for initializer,
// closure interior, type-conversion interior) are violations and must be
// replaced by either:
//
//  1. a constant from pkg/testutil/testtime (preferred for cross-cutting
//     timeouts: EventuallyDefault, MediumPoll, SelectShutdown, etc.); or
//  2. a package-level const at the top of the test file (for site-specific
//     deadlines such as ttlExpiryMargin = 5 used by `ttl * margin`).
//
// Exceptions:
//   - A BasicLit whose token value is "0" is not a violation (return 0 / var
//     x time.Duration = 0 is idiomatic zero-value usage).
//
// Platform scope:
//   - The gate runs on Linux CI (tools shard, governance verify). Files
//     gated behind //go:build darwin / //go:build windows are invisible to
//     the Linux build context and therefore not scanned. Other platforms
//     remain fully buildable and runnable; only static enforcement of this
//     invariant is Linux-only. See test-time-discipline.md.
//
// Companion gates:
//   - PROD-DURATION-CONST-01 enforces the same rule on production files
//     (the strict complement of "test code" per fileroles.IsProductionCode).
//     Together, the two gates leave no production-or-test code path where a
//     time.Duration literal can hide outside a package-level const.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 TEST-TIME-LITERAL-01
package archtest

import (
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
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
	// Intentionally not honoring testing.Short: the gate must be unstoppable
	// by environmental GOFLAGS=-short injection, since a silent skip would
	// produce a false-green in CI. The full scan is ~1-2 s after caching.

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

			rel, ok := fileroles.Rel(root, abs)
			if !ok || !fileroles.IsTestCode(rel) {
				continue
			}

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

// File-role classification is delegated to tools/internal/fileroles; see that
// package for the canonical predicates IsTestCode / IsProductionCode.
