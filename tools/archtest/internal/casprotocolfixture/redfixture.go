//go:build archtest_fixture

// Package casprotocolfixture contains intentionally-violating call sites
// against runtime/state/cas.NewProtocol that exercise the type-aware detector
// in cas_protocol_composition_root_test.go.
//
// Gated by the archtest_fixture build tag; production builds never see this
// file. The fixture is loaded by TestCASProtocol_RedFixtureDetected via
// archtest.RunTypedFixture (which injects the archtest_fixture tag inside its
// body).
//
// # Forms covered
//
// Every banned call appears in three callee shapes so the detector is exercised
// across the AST forms archtest.ResolvePackageRef resolves:
//
//   - qualified-import (`cas.NewProtocol(...)`)
//   - alias-import     (`casPkg.NewProtocol(...)`)
//   - dot-import       (`NewProtocol(...)` after `import . "…/cas"`)
//
// The Soft (pre-Medium) rule matched only the qualified form via Ident-name pkg
// matching (`sel.X.(*ast.Ident).Name == "cas"`); it missed the alias and dot
// shapes. The Medium rule resolves the callee to its *types.Func through
// types.Info.Uses and matches by owning package path, catching all three shapes.
package casprotocolfixture

import (
	"github.com/ghbvf/gocell/runtime/state/cas"
	casPkg "github.com/ghbvf/gocell/runtime/state/cas"
)

// dotImportNote: the dot-import form is in a sibling file (dotimport.go)
// because Go forbids `import . "X"` and `import "X"` in the same file.

func qualifiedCall() {
	_, _ = cas.NewProtocol(
		cas.WithVersionField("version"),
	)
}

func aliasedCall() {
	_, _ = casPkg.NewProtocol(
		casPkg.WithVersionField("version"),
	)
}
