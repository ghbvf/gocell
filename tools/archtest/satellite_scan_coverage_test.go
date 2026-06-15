//go:build archtest

// INVARIANT: SATELLITE-PARENT-PREFIX-SCAN-01
//
// SATELLITE-PARENT-PREFIX-SCAN-01 — anti-vacuity guard for the #1565 fix that
// stopped the satellite parent-prefix patterns "./cmd/...", "./adapters/...",
// and "./examples/..." from being SILENTLY SKIPPED in the post-split workspace.
//
// Background (F5): post-#1565 the repo root has no go.mod, and each of cmd/,
// adapters/, examples/ holds MULTIPLE go.work member modules (cmd/gocell +
// cmd/corebundle; adapters/postgres, adapters/redis, …). A "./cmd/..." pattern
// is owned by no single member, so the typed workspace loader's
// splitWorkspacePattern used to map it to skipPatternDir and DROP it (match-zero).
// Many security/authz funnels — fence-token mint, no-deleted-auth-symbols,
// credential-invalidate, capability-provider, … — declare coverage over exactly
// these satellite dirs; with the silent skip they passed VACUOUSLY (scanned
// nothing in cmd/adapters/examples). The shared satellite-aware loader
// packagesload.LoadWorkspace now expands each prefix to its real members so they are
// actually loaded.
//
// This guard loads the prefixes through the SAME typed path the funnels use
// (Run + Typed) and asserts each prefix yields packages from its member modules.
// A regression to silent-skip (or a future loader change that drops the
// expansion) turns this RED — the funnels can never again pass vacuously over a
// silently-empty satellite scan without this test failing first.
package archtest

import (
	"strings"
	"testing"
)

func TestSatelliteParentPrefixActuallyScanned(t *testing.T) {
	cases := []struct {
		pattern       string
		wantPkgSubstr string // an import-path substring that MUST appear in the scan
	}{
		{"./cmd/...", "/cmd/gocell"},
		{"./cmd/...", "/cmd/corebundle"},
		{"./adapters/...", "/adapters/postgres"},
		{"./examples/...", "/examples/"},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"~"+tc.wantPkgSubstr, func(t *testing.T) {
			var seen bool
			_ = Run(t, Typed(TypedOpts{Tests: false}, []string{tc.pattern}), func(p *Pass) []Diagnostic {
				if p.Pkg != nil && strings.Contains(p.Pkg.Path(), tc.wantPkgSubstr) {
					seen = true
				}
				return nil
			})
			if !seen {
				t.Fatalf("typed load of %q scanned no package matching %q — satellite coverage is being silently skipped "+
					"(F5 regression: a ./cmd/… ./adapters/… ./examples/… parent prefix is no longer expanded to its go.work members, "+
					"so security/authz funnels declaring this scope pass vacuously)", tc.pattern, tc.wantPkgSubstr)
			}
		})
	}
}
