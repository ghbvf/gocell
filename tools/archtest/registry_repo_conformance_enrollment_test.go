//go:build archtest

// INVARIANT: REGISTRY-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
//   - 实现扫描: types.Implements(*types.Interface) — type-aware，identifies every
//     concrete named type that satisfies ports.Registry (or *T).
//   - conformance 调用扫描: ResolvePackageRef + _test.go path filter — type-aware
//     callee resolution via *types.Info. Identifies every package with a
//     conformance.RunRegistryConformance call site.
//   - 综合: Medium 天花板 — Go cannot require a test to exist at compile time;
//     the enforcement is archtest-bound (CI fails), not compile-time. This is the
//     same documented ceiling as the accesscore repo-family siblings; Hard is
//     genuinely unreachable for "a test must exist and call the suite".
//
// Enforces (#2388): every concrete type in the production source tree that
// implements registrycore ports.Registry (mem.Registry + postgres.Registry) must
// have at least one conformance.RunRegistryConformance call in a _test.go file
// belonging to its package. A store that never enrolls is never held to the
// shared contract suite, which is exactly how a mem/PG semantic divergence
// (e.g. History nil vs empty — PR #2383 finding F16) slips through independent
// per-impl tests. This is the registrycore sibling of
// USERREPO-CONFORMANCE-ENROLLMENT-01, sharing checkRepoConformanceEnrollment +
// the runRepoEnrollmentREDFixture anti-vacuity proof.
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1 (reverse reflect-bait scan): OMITTED for this rule, by design. The
//     accesscore siblings scan production code for a string literal equal to the
//     interface name ("UserRepository" / "PolicyRepository" / "RoleRepository")
//     as a cheap reverse guard against a reflect.MethodByName-based implicit impl.
//     The registrycore interface name is the bare word "Registry", which appears
//     pervasively as a string literal in production code (type names, metric
//     labels, log messages, the "registrycore" cell id, the kernel registry
//     package, …), so the same scan would be massively vacuous-red — it cannot
//     distinguish reflect bait from ordinary usage. The core types.Implements
//     enrollment (the Medium guarantee) is unaffected; this only forgoes the
//     defense-in-depth reverse sentinel, which no production code triggers anyway.
//
//   - B2. generated mock implementations (mockery / gomock in _test.go) are
//     excluded: the single Tests=true load collects impls through a non-_test.go
//     declaration filter (productionImplCandidates / isTestDeclaredObj), so
//     _test.go-declared types are dropped. A generated mock in a production
//     non-test file would be flagged — intentionally.
//
//   - B3. embedded interface forwarding (struct embedding ports.Registry):
//     structurally satisfies the interface but provides no real storage; such
//     types appear only in test helpers (in _test.go, excluded from the impl scan).
//     A production struct embedding the interface is treated as an impl and must enroll.
//
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (sibling pattern)
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// INVARIANT: REGISTRY-CONFORMANCE-ENROLLMENT-01

// TestRegistryRepoConformanceEnrollment enforces REGISTRY-CONFORMANCE-ENROLLMENT-01:
// every concrete type implementing registrycore ports.Registry in the production
// tree must have a conformance.RunRegistryConformance call in a _test.go file of
// its package.
func TestRegistryRepoConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, ruleRegistryRepoConformanceEnrollment01,
		checkRepoConformanceEnrollment(t, registryRepoConformanceSpec(), ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestRegistryRepoConformanceEnrollment_REDFixture proves the enrollment detector
// is non-vacuous for ports.Registry: removing one impl's package from the enrolled
// set must flag exactly that package. The shared body lives in
// runRepoEnrollmentREDFixture (repo_conformance_enrollment_helpers_test.go).
func TestRegistryRepoConformanceEnrollment_REDFixture(t *testing.T) {
	t.Parallel()
	runRepoEnrollmentREDFixture(t, registryPortsPkg, registryRepoIfaceName)
}

// TestRegistryRepoConformanceEnrollment_ExpectedImplREDFixture proves the
// expectedImplPkgs anti-vacuity guard (#2388 F4) is non-vacuous: a collected impl
// set missing a pinned package (PG dropped) must be flagged, while the full set is
// accepted. This guards the "load-pattern gap leaves only mem enforced" risk the
// zero-impl guard alone cannot catch — a pure-logic synthetic case (no packages.Load).
func TestRegistryRepoConformanceEnrollment_ExpectedImplREDFixture(t *testing.T) {
	t.Parallel()
	spec := registryRepoConformanceSpec()
	require.NotEmpty(t, spec.expectedImplPkgs, "registry spec must pin expected impl packages (anti-vacuity)")

	// RED: PG impl package dropped from the collected set → flagged.
	memOnly := map[string]bool{registryMemPkg + ".Registry": true}
	assert.Contains(t, missingExpectedImplPkgs(spec.expectedImplPkgs, memOnly), registryPGPkg,
		"dropping the PG impl package must be flagged as a vacuous-green risk")

	// GREEN: both pinned packages present → nothing missing.
	full := map[string]bool{registryMemPkg + ".Registry": true, registryPGPkg + ".Registry": true}
	assert.Empty(t, missingExpectedImplPkgs(spec.expectedImplPkgs, full),
		"the full impl set must satisfy every pinned expected package")
}
