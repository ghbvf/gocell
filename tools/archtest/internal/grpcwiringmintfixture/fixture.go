//go:build archtest_fixture

// Package grpcwiringmintfixture is the GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01
// negative fixture. It mints the shared gRPC wiring singletons — the
// ServiceRegistrar and the DrainSignal — from a file that is NOT the sanctioned
// funnel (runtime/grpc/interceptor.NewServerInterceptors). This is the exact
// composition-root footgun the invariant forbids: a second mint site is the only
// way to obtain two registrars/drains and thereby wire a chain that reads one
// while the adapter binds the other (#1752).
//
// Loaded only under the archtest_fixture build tag; never imported from production
// code. The scanner pointed at this package must observe EXACTLY TWO mint call
// sites (one NewServiceRegistrar, one NewDrainSignal).
//
// DO NOT use this package in production code.
package grpcwiringmintfixture

import runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"

// badMint mints a registrar + drain outside the NewServerInterceptors funnel.
// Both calls are violations of GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01: in production
// only the funnel may mint these, so that the chains and the adapter provably
// share one instance of each.
func badMint() (*runtimegrpc.ServiceRegistrar, *runtimegrpc.DrainSignal) {
	reg := runtimegrpc.NewServiceRegistrar() // VIOLATION: registrar mint outside the funnel
	drain := runtimegrpc.NewDrainSignal()    // VIOLATION: drain mint outside the funnel
	return reg, drain
}
