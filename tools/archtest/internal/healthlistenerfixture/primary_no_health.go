//go:build archtest_fixture

// Package healthlistenerfixture is a deliberate SEC-FAIL-CLOSED-10 negative
// fixture loaded only when the archtest_fixture build tag is set.
//
// It references kernel/cell.PrimaryListener through a NON-default import alias
// (`kcell`) but never references cell.HealthListener — the exact shape
// SEC-FAIL-CLOSED-10 must flag (a composition root wiring the public listener
// without a dedicated health listener; bootstrap phase0 fail-fasts on it, see
// #673). The aliased import also proves the rule resolves listener references by
// canonical import path (typeseval.ResolvePackageRef → *types.PkgName →
// Imported().Path()), not by the syntactic identifier `cell`.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestSecurityDefaults/SEC-FAIL-CLOSED-10_fixture_catches_primary_without_health
// via archtest.RunTypedFixture.
//
// AI co-authors who modify this fixture MUST keep exactly one reference to
// cell.PrimaryListener and ZERO references to cell.HealthListener. Adding a
// HealthListener reference, or removing the PrimaryListener reference, breaks
// the companion test's primary==true / health==false contract.
package healthlistenerfixture

import kcell "github.com/ghbvf/gocell/kernel/cell"

// PrimaryRef references PrimaryListener through a non-default import alias. The
// value is never used at runtime — the fixture exists for AST/type-info
// analysis only.
var PrimaryRef = kcell.PrimaryListener
