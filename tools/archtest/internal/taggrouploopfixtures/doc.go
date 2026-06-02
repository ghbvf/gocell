// Package taggrouploopfixtures holds typed-loadable .go fixtures for
// TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01 (and its red/green reverse self-tests).
//
// Files in this package are loaded by
// tools/archtest/taggroup_loop_no_typed_run_test.go via typeseval.SharedResolver
// — the same typed pipeline the live archtest scan runs on production archtest
// *_test.go files. There is no syntactic fallback: callee identity in fixtures
// resolves through *types.Info exactly as it does for the live scan.
//
// This package is intentionally placed at
// tools/archtest/internal/taggrouploopfixtures/ (a non-_test.go internal
// sub-package) rather than under testdata/, for two reasons:
//
//  1. typed-load reachability: go/packages typed mode skips testdata directories
//     by default; an internal sub-package is loaded reliably by an explicit
//     pattern.
//
//  2. live-scan exclusion: the live TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01 scan
//     restricts to tools/archtest/*_test.go (the parent test file's direct
//     scope). Fixture files live one directory deeper and are not _test.go,
//     so they are filtered out — no risk of self-detection cycle.
//
// Naming convention: filenames starting with "red_" are intentional
// violations (the live rule must catch them); filenames starting with
// "green_" are compliant idioms (the live rule must NOT catch them).
// Functions declared as func _(...) so each file can drop the body in
// without naming pressure; multiple func _() per package are permitted by
// the Go spec.
//
// Naming: this package is named taggrouploopfixtures (rule-slug-fixtures).
// The sibling usage02fixtures package uses a numeric-suffix convention
// (rule-id-fixtures). Both conventions are accepted under
// tools/archtest/internal/; pick whichever reads naturally for the rule
// (kebab/slug for descriptive rule names, numeric suffix for ordered
// INVARIANT IDs).
package taggrouploopfixtures
