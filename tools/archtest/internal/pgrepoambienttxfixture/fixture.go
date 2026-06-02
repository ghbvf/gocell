//go:build archtest_fixture

// Package pgrepoambienttxfixture contains intentionally-violating struct
// definitions and function signatures that exercise the PG-REPO-AMBIENT-TX-01
// archtest. Gated by the archtest_fixture build tag; production builds never
// see this package.
//
// # File layout
//
//   - fixture.go (this file): package godoc + GREEN helpers. The file
//     extension is not _repo.go / _store.go so R1 / R2 do not scan it; R3 is
//     global but this file has no pgexec.ExecDirect callsite, so it produces
//     no diagnostics.
//   - fixture_repo.go: R1 + R2 RED cases (file-extension scoped) + R3 RED
//     call-bound cases + GREEN repo controls. The _repo.go suffix triggers
//     archtest R1/R2 scope.
//   - fixture_service.go: a NON-_repo.go file holding an R3 RED case — proves
//     R3 runs GLOBALLY (call-bound approval), unlike R1/R2 which stay
//     file-extension scoped.
//   - internal/pgexec/pgexec.go: sealed sub-package mirroring the production
//     form; provides the sealed PGExecutor interface, New factory, and the
//     call-bound ExecDirect(approval, …) function.
//
// The fixture is loaded by TestPGRepoAmbientTx_RedFixtureDetected via
// Run(t, Fixture(...)) (which injects the archtest_fixture build tag).
package pgrepoambienttxfixture
