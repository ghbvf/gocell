// authz_eval_clock_injected_test.go — bans wall-clock package functions in the
// ABAC PDP engine so environment time attributes always come from the injected
// clock.Clock.
//
//   - INVARIANT: AUTHZ-EVAL-CLOCK-INJECTED-01
//
// # What this guards
//
// The authorizationdecide engine resolves environment time attributes (hour,
// day_of_week) from an injected clock.Clock (NewService's mandatory clk param,
// read as s.clk.Now()). Reading the wall clock directly (time.Now / time.Since /
// time.Until) would make time-based policy decisions non-deterministic and
// untestable. This archtest forbids those package-level time functions in every
// production file under corecells/accesscore/slices/authorizationdecide/. Methods on
// time.Time (t.Hour(), t.Weekday()) are allowed — they read the injected reading,
// not the wall clock.
//
// # AI-robust rating (Medium, single axis)
//
// Detector is use-based (go/types info.Uses → *types.Func with nil receiver and
// pkg path "time"): the package-qualified form (time.Now), an import-aliased
// form, AND the dot-imported bare ident all resolve to the same *types.Func, so
// no import shape can slip a wall-clock read past the ban. Rated Medium: it is an
// archtest (not a type-system seal); the Hard ceiling is the same form-lock as
// SAGA-EXECUTOR-RAND-INJECTED-01 (the injected-source archtest IS the highest
// reachable form for "no global wall-clock read"; no cheaper Hard upgrade exists,
// so no Hard-upgrade issue is opened — same as the saga rand ceiling).
//
// # Detection + anti-vacuity
//
// A RED fixture (internal/authzclockfixture, gated behind the archtest_fixture
// build tag) calls time.Now() and the reverse self-check asserts the detector
// fires on it — the liveness proof for this ban-type rule.
//
// # Tool blind spot (charter §"强制盲区自检")
//
//   - A wall-clock read assembled by reflection, or via a non-time package that
//     internally calls time.Now, is invisible — the same known limit as every
//     identifier-resolution funnel in this suite. Both the package-qualified and
//     dot-import textual forms ARE covered (use-based resolution).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// authzEvalRelPrefix is the module-relative path prefix of the ABAC PDP engine
// production files scanned by AUTHZ-EVAL-CLOCK-INJECTED-01.
const authzEvalRelPrefix = "corecells/accesscore/slices/authorizationdecide/"

// authzEvalBannedTimeFuncs is the set of package-level time functions forbidden
// in the engine; time must come from the injected clock.Clock.
var authzEvalBannedTimeFuncs = map[string]struct{}{
	"Now":   {},
	"Since": {},
	"Until": {},
}

// TestAuthzEvalClockInjected01 asserts no production engine file references a
// banned package-level time function.
func TestAuthzEvalClockInjected01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanAuthzEvalBannedTime(p, authzEvalRelPrefix)
	})
	Report(t, "AUTHZ-EVAL-CLOCK-INJECTED-01", diags)
}

// scanAuthzEvalBannedTime flags every use of a banned package-level time function
// in files whose module-relative path begins with relPrefix.
func scanAuthzEvalBannedTime(p *Pass, relPrefix string) []Diagnostic {
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if !strings.HasPrefix(rel, relPrefix) {
			continue
		}
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			if !isAuthzEvalBannedTimeFunc(p.TypesInfo, id) {
				return
			}
			pos := p.Fset.Position(id.Pos())
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"AUTHZ-EVAL-CLOCK-INJECTED-01: time.%s is called in %s. The ABAC engine must read time from the "+
						"injected clock.Clock (s.clk.Now()), never the wall clock, so time-based policy decisions stay "+
						"deterministic and testable. Use the injected clock.", id.Name, rel,
				),
			})
		})
	}
	return d
}

// isAuthzEvalBannedTimeFunc reports whether id resolves to a banned package-level
// function of the standard "time" package (nil receiver excludes time.Time
// methods, which legitimately operate on the injected reading).
func isAuthzEvalBannedTimeFunc(info *types.Info, id *ast.Ident) bool {
	if _, banned := authzEvalBannedTimeFuncs[id.Name]; !banned {
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() != nil {
		// Method on time.Time (t.Hour() etc.) — allowed; only package-level
		// wall-clock functions are banned.
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == "time"
}

// TestAuthzEvalClockInjected01_RedFixture is the reverse self-check: the fixture
// calls time.Now(); the detector must flag it. A 0 result means the detector
// regressed.
func TestAuthzEvalClockInjected01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	const fixtureRel = "tools/archtest/internal/authzclockfixture/"
	pattern := "./tools/archtest/internal/authzclockfixture/..."

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		found += len(scanAuthzEvalBannedTime(p, fixtureRel))
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: detector must flag the fixture's time.Now() call")
}
