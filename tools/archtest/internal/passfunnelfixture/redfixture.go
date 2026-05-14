//go:build archtest_fixture

// Package passfunnelfixture contains intentionally-violating archtest entry
// point usages that exercise the three PASS-FUNNEL-* meta-archtest rules in
// pass_funnel_test.go. The package is gated by the archtest_fixture build
// tag so it is invisible to default builds (and therefore invisible to the
// production PASS-FUNNEL scan, which loads only the default build context).
//
// TestPassFunnel_FixtureCoverage loads this package with the archtest_fixture
// tag via typeseval.SharedResolver and asserts each rule emits ≥ 1 diagnostic.
// Removing any of the three violation lines below turns one of the rule's
// coverage assertions red — locking the rule pipeline at the live-AST level
// rather than the data-snapshot level (per AI-rebust charter "盲区自检").
package passfunnelfixture

import (
	"go/parser"
	"testing"

	// VIOLATION: PASS-FUNNEL-PACKAGES-IMPORT-01 — direct import of packages.
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// PassFunnelRed combines all three banned forms inside a single function.
// The function is never called at runtime (build-tag-gated). It must compile
// so packages.Load can type-check it; type-checked AST is what drives the
// PASS-FUNNEL rule pipeline.
func PassFunnelRed(t *testing.T) {
	t.Helper()

	// VIOLATION: PASS-FUNNEL-EACHFILE-01
	scanner.EachFile(t, scanner.ModuleScope(""), parser.SkipObjectResolution,
		func(*testing.T, scanner.FileContext) {})

	// VIOLATION: PASS-FUNNEL-LOADPACKAGES-01 (LoadPackages)
	_, _, _ = typeseval.LoadPackages("", false, nil, ".")

	// VIOLATION: PASS-FUNNEL-LOADPACKAGES-01 (SharedResolver)
	_, _ = typeseval.SharedResolver("", false, nil, ".")

	// Force packages reference so the import is not elided by the compiler.
	_ = packages.Config{}
}
