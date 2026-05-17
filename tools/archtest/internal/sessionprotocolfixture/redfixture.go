//go:build archtest_fixture

// Package sessionprotocolfixture contains intentionally-violating call sites
// against runtime/auth/session.NewProtocol that exercise the type-aware
// detector in session_protocol_composition_root_test.go. (session.MustNewProtocol
// was deleted by B2-K-02; only NewProtocol remains.)
//
// Gated by the archtest_fixture build tag; production builds never see this
// file. The fixture is loaded by TestSessionProtocol_RedFixtureDetected via
// archtest.RunTypedFixture (which injects the archtest_fixture tag inside
// its body).
//
// # Forms covered
//
// Every banned call appears in three callee shapes so the detector is
// exercised across the AST forms typeseval.ResolvePackageRef resolves:
//
//   - qualified-import (`session.NewProtocol(...)`)
//   - alias-import     (`sess.NewProtocol(...)`)
//   - dot-import       (`NewProtocol(...)` after `import . "…/session"`)
//
// The Soft (pre-Medium) rule matched only the qualified form via
// Ident-name pkg matching (`sel.X.(*ast.Ident).Name == "session"`); it
// missed the alias and dot shapes. The Medium rule resolves the callee
// to its *types.Func through types.Info.Uses and matches by owning
// package path, catching all three shapes.
package sessionprotocolfixture

import (
	"github.com/ghbvf/gocell/runtime/auth/session"
	sess "github.com/ghbvf/gocell/runtime/auth/session"
)

// dotImportAlias keeps a second import of runtime/auth/session under a dot
// alias so the bare-Ident shape can be exercised. Go forbids two named
// imports of the same path in one file, and dot + named cannot coexist for
// the same path either — so we put the dot-import form in a sibling file.

func qualifiedCalls() {
	_, _ = session.NewProtocol()
}

func aliasedCalls() {
	_, _ = sess.NewProtocol()
}
