//go:build archtest

// repo_conformance_enrollment_helpers_test.go — shared anti-vacuity RED fixture
// for the repo-family conformance-enrollment rules (POLICYREPO / ROLEREPO /
// USERREPO / REGISTRY). Each rule's *_REDFixture test is a one-line call into
// runRepoEnrollmentREDFixture, so the ~80-line "load iface + simulate missing
// enrollment + assert flagged" body lives in ONE place and cannot drift across
// the four members (#2388 consolidation; previously each rule file duplicated it).
package archtest

import (
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// runRepoEnrollmentREDFixture verifies the enrollment detection logic flags an
// implementation when the owning package is not in the enrolledPkgs set. It
// exercises the core of checkRepoConformanceEnrollment without a standalone
// fixture module (the ports interfaces live in internal packages, making
// cross-module fixture modules impossible).
//
// Strategy: load the real iface (from portsPkg) + its production impls from the
// tree, then simulate a "missing enrollment" by removing one impl's package from
// the enrolled set. Assert flagUnenrolledByPkg reports exactly that package.
// Parameterized by (portsPkg, ifaceName) so every repo-family member proves its
// own detector is non-vacuous against its real impl set.
func runRepoEnrollmentREDFixture(t *testing.T, portsPkg, ifaceName string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)

	// corecells is a separate go module, so prodscan.Patterns's root-relative
	// ./... does not cross the module boundary into it — the explicit
	// ./corecells/... is required to load the iface + impls in one packages.Load.
	ifacePatterns := append([]string{"./corecells/..."}, prodscan.Patterns(root)...)

	var iface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == portsPkg {
				iface = lookupNamedIface(p.Pkg, ifaceName)
				return nil
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, iface, "REDFixture: could not resolve %s interface in %s", ifaceName, portsPkg)

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectImplsFromScope(pkg, iface, true, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty (need at least one impl)")

	// Pick the first impl key and derive its pkg path.
	var targetImplKey string
	for k := range implSet {
		targetImplKey = k
		break
	}
	dotIdx := strings.LastIndex(targetImplKey, ".")
	require.Greater(t, dotIdx, 0, "REDFixture: malformed impl key %q", targetImplKey)
	targetPkg := targetImplKey[:dotIdx]

	// Simulate missing enrollment: enrolledPkgs contains all impls EXCEPT targetPkg.
	enrolledPkgs := make(map[string]bool)
	for pkg := range implPkgSet {
		if pkg != targetPkg {
			enrolledPkgs[pkg] = true
		}
	}

	// Run the real flagging logic with the simulated enrolled set.
	diags := flagUnenrolledByPkg(implSet, enrolledPkgs,
		func(implKey, _ string) string { return implKey + " not enrolled" })

	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing pkg %q from enrolledPkgs must produce at least 1 violation, got 0", targetPkg)

	// Extra: confirm at least one diagnostic targets the removed pkg.
	var foundTarget bool
	for _, d := range diags {
		if strings.HasPrefix(d.Rel, targetPkg) {
			foundTarget = true
			break
		}
	}
	assert.True(t, foundTarget,
		"REDFixture: expected at least one diagnostic with Rel prefix %q, got %v", targetPkg, diags)
}
