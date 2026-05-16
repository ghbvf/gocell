// INVARIANT: TEST-TIME-LITERAL-01
//
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

	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// TestTestTimeLiteralConst enforces TEST-TIME-LITERAL-01 using the same
// universal AST walk as PROD-DURATION-CONST-01: for every declaration that
// is not a package-level const block, any expression whose static type is
// time.Duration and whose subtree contains a BasicLit is a violation.
//
// The only difference from PROD-DURATION-CONST-01 is the file filter: we
// include exactly the files PROD-DURATION-CONST-01 excludes as "test code".
//
// Build tags: typeseval.FlatNonDefaultTags() returns the union of every
// tag in typeseval.KnownNonDefaultTags() (single source); a fail-closed
// self-test (TestKnownNonDefaultTagsCoverage in typeseval) refuses to
// pass when a //go:build directive references a tag not on that list, so
// new tagged test files with literal durations cannot silently bypass the
// invariant.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6
func TestTestTimeLiteralConst(t *testing.T) {
	t.Parallel()
	// Intentionally not honoring testing.Short: the gate must be unstoppable
	// by environmental GOFLAGS=-short injection, since a silent skip would
	// produce a false-green in CI. The full scan is ~1-2 s after caching.

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	var violations []string
	RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, patterns,
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsTestCode(rel) {
					continue
				}
				violations = append(violations,
					scanProdDurationAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	if len(violations) > 0 {
		t.Errorf("TEST-TIME-LITERAL-01: extract test-time durations to a package-level const "+
			"(prefer pkg/testutil/testtime.* for cross-cutting timeouts; declare a "+
			"file-local package-level const for site-specific deadlines). "+
			"ref: docs/plans/202605011500-029-master-roadmap.md G6\n"+
			"%d violation(s) found", len(violations))
	}
}

// File-role classification is delegated to tools/internal/fileroles; see that
// package for the canonical predicates IsTestCode / IsProductionCode.
