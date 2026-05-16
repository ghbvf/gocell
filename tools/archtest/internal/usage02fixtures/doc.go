// Package usage02fixtures holds typed-loadable .go fixtures for
// SCANNER-FRAMEWORK-USAGE-02 (and its BS1/BS2/BS3 reverse self-tests).
//
// Files in this package are loaded by tools/archtest/scanner_framework_usage_test.go
// via typeseval.SharedResolver — the same typed pipeline the live archtest scan
// runs on production archtest *_test.go files. There is no syntactic fallback:
// callee identity in fixtures resolves through *types.Info exactly as it does
// for the live scan, removing the PR-505 Soft fallback that previously served
// inline-source fixtures.
//
// This package is intentionally placed at tools/archtest/internal/usage02fixtures/
// (a non-_test.go internal sub-package) rather than under testdata/, for two
// reasons:
//
//  1. typed-load reachability: go/packages typed mode skips testdata directories
//     by default; an internal sub-package is loaded reliably by the same
//     ./tools/archtest/... pattern the live scan uses.
//
//  2. live-scan exclusion: the USAGE-02 live scan keeps only files whose parent
//     directory is exactly "tools/archtest" AND whose name ends in _test.go.
//     Fixture files live one directory deeper and are not _test.go, so they
//     are filtered out — no risk of self-detection cycle.
//
// Each .go file in this package corresponds to one fixture case; the basename
// (without .go) is the case identifier passed to loadFixture02. Functions are
// declared as func _(...) so each file can drop the body in without naming
// pressure; multiple func _() per package are permitted by the Go spec.
package usage02fixtures
