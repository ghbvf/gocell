// Package usage02fixtures holds typed-loadable .go fixtures for
// SCANNER-FRAMEWORK-USAGE-02 (main detector + BS1/BS2/BS3 reverse self-tests
// + BS4/BS5 main-detector-handled forms, on both EachInChildren and
// EachInSubtree depth axes).
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
//
// # Fixture naming convention
//
// Files are named `<role>_<callee>[_<axis>]_<shape>.go`:
//
//	<role>: one of
//	  red_   — exercises the MAIN detector; loadFixture02 expects wantHits ≥ 1
//	  green_ — exercises GREEN form (canonical FindFirstChild/FindFirstInSubtree
//	           or sentinel-free EachIn* iteration); wantHits = 0
//	  bs1_/bs2_/bs3_ — BS1/BS2/BS3 blind-spot shape; MAIN detector
//	           expects wantHits = 0, the BS reverse detector
//	           (closureDoneSentinelBlindSpots) expects wantHits ≥ 1
//	  bs4_   — BS4 nested-walker shape; caught by MAIN detector (outer + inner)
//	<callee>: scanner or archtest (the package façade under test)
//	<axis>:   omitted for the original EachInChildren axis; added as
//	          _eachinsubtree_ for the EachInSubtree axis. The shape is
//	          depth-agnostic — the same detector covers both axes — but
//	          having an axis-tagged fixture per BS shape anchors that both
//	          depth axes are validated end-to-end through the typed pipeline.
//	<shape>:  short shape descriptor (done_sentinel, nonliteral_rhs,
//	          else_guard, assign_outside, nested_inner_sentinel,
//	          bs5_helper, ...).
package usage02fixtures
