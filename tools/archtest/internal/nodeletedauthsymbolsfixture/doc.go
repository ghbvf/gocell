//go:build archtest_fixture

// Package nodeletedauthsymbolsfixture is the deliberate
// NO-DELETED-AUTH-SYMBOLS-01 negative fixture loaded only when the
// archtest_fixture build tag is set.
//
// The fixture exercises every import form the typed scanner must catch:
//
//   - default alias            (caller_default_alias.go)
//   - custom alias             (caller_custom_alias.go)
//   - dot-import (func only)   (caller_dot_import.go)
//   - same-name foreign pkg    (caller_negative_other_pkg.go) — must NOT hit
//
// Sub-packages auth/ and notauth/ provide local fake `RoleInternalAdmin`,
// `ServiceNameInternal`, and `BuiltinServiceRoles` symbols. The fixture-local
// "auth" import path lives in this internal/* tree — the rule's real-repo
// target is runtime/auth, but the scanner is parametrized over an arbitrary
// authImportPath so the fixture can exercise the same code path with a
// distinct package identity.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestNoDeletedAuthSymbols_FixtureCatchesAllForms via
//
//	archtest.Run(t, archtest.Fixture(archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/nodeletedauthsymbolsfixture/..."}), rule)
//
// AI co-authors who modify the fixture must keep the hit counts in sync with
// TestNoDeletedAuthSymbols_FixtureCatchesAllForms (exact-count assertion).
// Adding a call site requires bumping the expected count; the test will
// otherwise fail and force review.
//
// ref: tools/archtest/internal/auditledgerfixture (sibling pattern)
// ref: tools/archtest/internal/capfunnelfixture (multi-form coverage)
package nodeletedauthsymbolsfixture
