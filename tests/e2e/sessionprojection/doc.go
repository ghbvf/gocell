// Package sessionprojection holds the e2e capability test for the accesscore
// session_registry CQRS projection (EPIC #1504 PR-04, #1771) — the platform's
// first durable-by-default projection. Real test bodies live in
// session_registry_e2e_test.go behind `//go:build e2e && pg` so demo-mode CI
// (no `pg` build tag) sees this directory as an empty Go package and `go test`
// emits a "[no test files]" event that e2egate's per-package no-test-files
// exception silently ignores.
//
// Capability gating model (mirror of tests/e2e/encryption): the durable
// projection only wires under the postgres topology (the gate removed in #1771
// makes it the production default once a projection is declared). When the e2e
// workflow runs with `-tags=e2e,pg` + GOCELL_E2E_PG_AVAILABLE=1 the test below
// is required to execute; if the capability is requested but the env is missing,
// require.PG skips it — and because this package's only test lives here,
// e2egate's "package declared tests but every one was skipped" rule fires and
// the gate goes red. This reuses the existing per-package rule rather than new
// gate plumbing.
package sessionprojection
