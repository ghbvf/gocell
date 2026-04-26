// Package encryption holds the e2e capability tests for the configcore
// PG pilot's value-encryption pipeline. Real test bodies live in
// encryption_e2e_test.go behind `//go:build e2e && pg` so demo-mode CI
// (no `pg` build tag) sees this directory as an empty Go package and
// `go test` emits a "[no test files]" event that e2egate's per-package
// no-test-files exception silently ignores.
//
// Capability gating model: when PR-CFG-G G.8 lands the accesscore real-
// outbox wiring and the e2e workflow flips to `-tags=e2e,pg` plus
// GOCELL_E2E_PG_AVAILABLE=1, the tests below are required to execute.
// If the env is misconfigured (capability requested but env missing),
// require.PG skips them — and because this package's only tests live
// here, e2egate's "package declared tests but every one was skipped"
// rule fires and the gate goes red. That is the capability gate the
// reviewer's P1 called for, implemented through the existing per-
// package rule rather than new gate plumbing.
package encryption
