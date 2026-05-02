// PROD-CLOCK-INJECTION-01 — invariant-driven gate for *production* code.
//
// Invariant: In every Go file whose role is "production code"
// (tools/internal/fileroles.IsProductionCode), there must be no direct
// reference to one of the wall-clock entry points in the standard library's
// time package — neither as a call (time.Now()) nor as a function value
// (now := time.Now). All wall-clock interactions must flow through an
// injected kernel/clock.Clock.
//
// The forbidden set is exactly the set of stdlib time symbols that bypass
// the abstraction:
//
//   - time.Now      — read absolute time
//   - time.Since    — elapsed since
//   - time.Until    — remaining until
//   - time.NewTimer — single-fire timer
//   - time.NewTicker — periodic ticker
//   - time.After    — channel-based single-fire
//   - time.AfterFunc — callback-based single-fire
//   - time.Tick     — leaky periodic ticker (stdlib-deprecated)
//   - time.Sleep    — uncancellable blocking sleep
//
// Resolution is type-driven: every *ast.SelectorExpr and bare *ast.Ident is
// run through go/types.Info.ObjectOf to obtain the resolved *types.Func, then
// gated on obj.Pkg().Path() == "time" and obj.Name() ∈ forbiddenTimeFns. This
// makes the check immune to import aliases ("import t \"time\"; t.Now") and
// dot-imports ("import . \"time\"; Now()"), which an identifier-name
// heuristic cannot cover.
//
// Whitelist:
//   - kernel/clock/ — owns the canonical Real() implementation; the only
//     legitimate package that may delegate to the stdlib time API.
//   - kernel/clock/clockmock/ — owns the deterministic test fake.
//
// pkg/securecookie/ used to be exempt while it carried a local realClock
// fallback. As of PR #348 Round 2, securecookie deletes the fallback and
// requires explicit WithClock injection (Encode/Decode call MustHaveClock at
// entry); the package no longer references stdlib time symbols directly, so
// the exemption was removed.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
// ref: dominikh/go-tools analysis/code/code.go CallName / IsCallToAny
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

// allowedRealClockPaths lists the prefixes whose files may legitimately
// reference stdlib time symbols directly. See package doc for rationale.
var allowedRealClockPaths = []string{
	"kernel/clock/",
}

// forbiddenTimeFns maps each forbidden stdlib time function to the equivalent
// Clock interface method that production callers must use instead.
//
// Note that time.After is mapped to NewTimerAt because the channel-returning
// shortcut form has no ctx-aware analog; callers must rewrite as
// `timer := clk.NewTimerAt(deadline); defer timer.Stop()` +
// `select { case <-ctx.Done(): ... case <-timer.C(): ... }`.
//
// time.NewTicker and time.Tick map to clock.Clock.NewTicker (interval-based,
// matching stdlib semantics: first fire at Now()+interval). The Clock
// interface deliberately does not expose a "TickerAt" form — the
// duration-based shape carries the same read-then-act gap on the very first
// tick that NewTimerAt was designed to eliminate, but for tickers the
// stdlib parity is more valuable than the absolute-deadline guarantee.
var forbiddenTimeFns = map[string]string{
	"Now":       "clock.Clock.Now",
	"Since":     "clock.Clock.Since",
	"Until":     "clock.Clock.Until",
	"NewTimer":  "clock.Clock.NewTimerAt",
	"NewTicker": "clock.Clock.NewTicker",
	"After":     "clock.Clock.NewTimerAt",
	"AfterFunc": "clock.Clock.AfterFunc",
	"Tick":      "clock.Clock.NewTicker",
	"Sleep":     "clock.Clock.Sleep",
}

// TestProdClockInjection enforces PROD-CLOCK-INJECTION-01 against the
// production tree. ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6.
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

			violations = append(violations, scanProdClockInjectionAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"PROD-CLOCK-INJECTION-01: production code must call clk.Now / "+
			"clk.Since / clk.Until / clk.NewTimerAt / clk.NewTicker / "+
			"clk.AfterFunc / clk.Sleep on an injected kernel/clock.Clock; "+
			"only kernel/clock itself may delegate to the stdlib time package. "+
			"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md")
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

// scanProdClockInjectionAST walks file's AST and returns a sorted slice of
// violation strings ("rel:line: detail") for every reference to one of the
// forbidden stdlib time functions. Both call positions (time.Now()) and
// function-value positions (now := time.Now, struct field assignment
// `{now: time.Now}`) are detected. Detection is type-driven via
// info.ObjectOf, so import aliases and dot-imports are covered uniformly.
//
// scanProdClockInjectionAST is exported within the archtest package so the
// fixture-based regression tests in prod_clock_injection_fixtures_test.go can
// share the exact same predicate.
func scanProdClockInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []string {
	var out []string
	seen := map[string]bool{}

	record := func(node ast.Node, name string) {
		line := fset.Position(node.Pos()).Line
		key := fmt.Sprintf("%s:%d:%s", rel, line, name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, fmt.Sprintf(
			"%s:%d: time.%s — must use injected %s instead",
			rel, line, name, forbiddenTimeFns[name]))
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.SelectorExpr:
			// Standard form: time.Now, t.Now (alias), pkg.Now (any pkg).
			// Type info on .Sel resolves the actual function regardless of
			// the receiver identifier name.
			if name, ok := matchedTimeFn(info, e.Sel); ok {
				record(e, name)
				return false // do not descend into the resolved children
			}
		case *ast.Ident:
			// Dot-import form: `import . "time"; Now()`. The Ident is the
			// call function reference itself — no SelectorExpr surrounds it.
			if name, ok := matchedTimeFn(info, e); ok {
				record(e, name)
			}
		}
		return true
	})

	sort.Strings(out)
	return out
}

// matchedTimeFn reports whether ident resolves (via info.ObjectOf) to a
// package-level function in the standard library's time package whose name is
// in the forbidden set.
//
// Filters explicitly to *types.Func so that legitimate references to
// time.Time (a Type), time.Second (a Const), or any same-named field on a
// non-time package are not flagged.
//
// Methods on time.Time (e.g. t.After(u), t.Before(u)) share the same name as
// the forbidden package-level functions but are semantically unrelated — they
// compare two time.Time values and return bool. They are excluded by checking
// Signature().Recv(): a non-nil receiver indicates a method, not a package-level
// function. Production code should use these method comparisons freely.
func matchedTimeFn(info *types.Info, ident *ast.Ident) (string, bool) {
	if info == nil || ident == nil {
		return "", false
	}
	fn, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return "", false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != "time" {
		return "", false
	}
	// Exclude methods (e.g. time.Time.After, time.Time.Before) — only flag
	// package-level functions such as time.After(d Duration) <-chan time.Time.
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return "", false
	}
	name := fn.Name()
	if _, listed := forbiddenTimeFns[name]; !listed {
		return "", false
	}
	return name, true
}
