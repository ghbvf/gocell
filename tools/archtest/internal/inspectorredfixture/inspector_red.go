//go:build archtest_fixture

// Package inspectorredfixture is a deliberate violation surface that drives
// real-package coverage of forbiddenMethodSymbols[golang.org/x/tools/go/ast/
// inspector] in scanner_framework_usage_test.go.
//
// The archtest_fixture build tag excludes this package from `go build ./...`
// and `go test ./...` so the deliberate banned-method calls below never
// pollute real-repo scans, lint, or coverage. It is loaded explicitly by
// TestScannerFrameworkUsage01_InspectorMethodBanLive via
//
//	archtest.RunTypedFixture(t, archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/inspectorredfixture"}, rule)
//
// Sister fixture packages (wrapfixture/violation, rawparamfixture,
// wrapfixture/kernelcellsibling) follow the same convention.
//
// SCANNER-FRAMEWORK-USAGE-01 only scans tools/archtest/<file>_test.go (parent
// dir exact match); this internal sub-package is out of scope of the live
// rule itself. TestScannerFrameworkUsage01_InspectorMethodBanLive loads this
// package via typeseval.LoadPackages so the *types.Info has full method-set
// information for *inspector.Inspector (which importer.Default() in
// runFixture cannot resolve for non-stdlib paths), then runs forbiddenWalkRefs
// against it and asserts 4 diagnostics — one per banned method on the
// inspector receiver. Removing the inspector entry from forbiddenMethodSymbols
// turns the test red, locking the data row Hard.
package inspectorredfixture

import (
	"go/ast"

	"golang.org/x/tools/go/ast/inspector"
)

// callBannedMethods is never invoked at runtime; it exists purely as
// type-checkable scaffolding so go/types populates info.Selections for the
// (*inspector.Inspector) method calls. Each line below is a banned shape:
//
//   - Preorder    — type-walk visitor
//   - Nodes       — push/pop visitor
//   - WithStack   — stack-aware visitor
//   - PreorderSeq — iter.Seq variant (Go 1.23+)
//
// Lockstep invariant: TestScannerFrameworkUsage01_InspectorMethodBanLive
// asserts exactly len(diags) == 4 — one per method call here. Adding a new
// banned method to forbiddenMethodSymbols[inspector] in
// scanner_framework_usage_test.go requires adding a matching call site here
// AND bumping wantHits in the live test.
func callBannedMethods(insp *inspector.Inspector) {
	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(ast.Node) {})
	insp.Nodes([]ast.Node{(*ast.FuncDecl)(nil)}, func(ast.Node, bool) bool { return true })
	insp.WithStack([]ast.Node{(*ast.FuncDecl)(nil)}, func(ast.Node, bool, []ast.Node) bool { return true })
	for n := range insp.PreorderSeq((*ast.FuncDecl)(nil)) {
		_ = n
	}
}

// var _ = callBannedMethods anchors the function so the Go compiler does
// not report it as unused; the function must NEVER be invoked at runtime
// (it would dereference a nil *Inspector and panic) — its sole purpose is
// to make go/types populate info.Selections for archtest detection.
var _ = callBannedMethods
