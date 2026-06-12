//go:build archtest_fixture

// fixture_dotimport.go exercises the BARE-IDENTIFIER reference form of the
// GRPC-WIRING-* guards. A dot-import of runtime/grpc makes NewServiceRegistrar /
// NewDrainSignal / NewServerInterceptorsBundle resolve as bare *ast.Ident calls —
// the exact AST/types shape of a same-package call inside runtime/grpc itself (the
// package that DEFINES these constructors, hence the most reachable bypass). A
// selector-only scanner would miss these; collectGRPCWiringRefs must resolve them
// via TypesInfo.Uses. This is the non-vacuity proof for the bare-ident branch
// (#1752 F1), separate from fixture.go which covers the cross-package selector form.
//
// DO NOT use this package in production code.
package grpcwiringmintfixture

import (
	"google.golang.org/grpc"

	. "github.com/ghbvf/gocell/runtime/grpc"
)

// badMintBareIdent mints via BARE identifiers (dot-import) — the shape a
// same-package runtime/grpc helper would have. Two violations of
// GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01.
func badMintBareIdent() (*ServiceRegistrar, *DrainSignal) {
	reg := NewServiceRegistrar() // VIOLATION: bare-ident registrar mint
	drain := NewDrainSignal()    // VIOLATION: bare-ident drain mint
	return reg, drain
}

// badBundleBareIdent assembles a bundle via a BARE identifier — one violation of
// GRPC-WIRING-BUNDLE-CALLER-01.
func badBundleBareIdent() ServerInterceptors {
	reg, drain := badMintBareIdent()
	return NewServerInterceptorsBundle( // VIOLATION: bare-ident bundle assembly
		[]grpc.ServerOption{grpc.EmptyServerOption{}}, reg, drain)
}
