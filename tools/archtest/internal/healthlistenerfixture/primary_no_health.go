//go:build archtest_fixture

// Command healthlistenerfixture is a deliberate SEC-FAIL-CLOSED-10 negative
// fixture loaded only when the archtest_fixture build tag is set.
//
// It is a `package main` (a composition root) that references
// kernel/cell.PrimaryListener through a NON-default import alias (`kcell`) but
// never references cell.HealthListener — the exact shape SEC-FAIL-CLOSED-10 must
// flag (a composition root wiring the public listener without a dedicated health
// listener; bootstrap phase0 fail-fasts on it, see #673). Being `package main`
// matters: the fixture drives the full rule via sec10Violations, exercising the
// `p.Pkg.Name() == "main"` gate in addition to the primary-without-health
// detection. The aliased import also proves the rule resolves listener references
// by canonical import path (ResolvePackageRef → *types.PkgName → Imported().Path()),
// not by the syntactic identifier `cell`.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestSecurityDefaults/SEC-FAIL-CLOSED-10_fixture_catches_primary_without_health
// via archtest.RunTypedFixture.
//
// AI co-authors who modify this fixture MUST keep it `package main`, keep exactly
// one reference to cell.PrimaryListener, and keep ZERO references to
// cell.HealthListener. Adding a HealthListener reference, removing the
// PrimaryListener reference, or changing the package clause breaks the companion
// test's "exactly one SEC-FAIL-CLOSED-10 violation" contract.
package main

import kcell "github.com/ghbvf/gocell/kernel/cell"

// primaryRef references PrimaryListener through a non-default import alias. The
// value is never used at runtime — the fixture exists for AST/type-info
// analysis only.
var primaryRef = kcell.PrimaryListener

func main() { _ = primaryRef }
