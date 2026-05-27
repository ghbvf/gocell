//go:build archtest_fixture

// Package pgrepoambienttxfixture contains intentionally-violating struct
// definitions and function signatures that exercise the PG-REPO-AMBIENT-TX-01
// archtest. Gated by the archtest_fixture build tag; production builds never
// see this package.
//
// # File layout
//
//   - fixture.go (this file): package godoc + GREEN helpers. The file
//     extension is not _repo.go / _store.go so R1 / R2 / R3 do not scan it;
//     anything here is implicitly out of scope.
//   - fixture_repo.go: ALL RED cases (R1 + R2 + R3) plus GREEN repo controls.
//     The _repo.go suffix triggers archtest scope.
//   - internal/pgexec/pgexec.go: sealed sub-package mirroring the production
//     form; provides PGExecutor interface and New factory.
//
// The fixture is loaded by TestPGRepoAmbientTx_RedFixtureDetected via
// archtest.RunTypedFixture (which injects the archtest_fixture build tag).
package pgrepoambienttxfixture
