//go:build archtest_fixture

// Package eventtransportkindmintfixture is the RED fixture for
// EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01.
//
// It references the real-broker minter bootstrap.RealBrokerEventTransport from
// OUTSIDE cellmodules/eventtransport — the sole sanctioned minter — two ways:
// a direct call (ForgeRealBroker) and a function-value reference
// (ForgeViaFuncValue, the #2215 F1 regression case that a CallExpr.Fun-only scan
// would miss). Both are the forge the funnel must flag: a composition root (or any
// package) that hands the phase0 split-topology gate a "real broker" claim without
// going through eventtransport.Resolve, which mints it only when it actually
// constructs the RabbitMQ transport (#2211).
//
// InMemoryEventTransport() is the GREEN control: it is the safe direction (a
// split topology carrying it is rejected by the gate), so the scanner must NOT
// flag it.
//
// DO NOT use this package in production code.
package eventtransportkindmintfixture

import "github.com/ghbvf/gocell/framework/runtime/bootstrap"

// ForgeRealBroker mints the real-broker kind outside eventtransport via a direct
// call — the violation the funnel must catch.
func ForgeRealBroker() bootstrap.EventTransportKind {
	return bootstrap.RealBrokerEventTransport()
}

// ForgeViaFuncValue launders the minter through a function value before calling
// it (#2215 F1): the call expression's Fun is the local `mint`, so a
// CallExpr.Fun-only scan would MISS this. The funnel's SelectorExpr-level scan
// resolves the `bootstrap.RealBrokerEventTransport` reference on the assignment
// RHS, so it is still flagged. This is the regression guard for the func-value
// blind spot.
func ForgeViaFuncValue() bootstrap.EventTransportKind {
	mint := bootstrap.RealBrokerEventTransport
	return mint()
}

// SafeInMemory mints the in-memory kind — the GREEN control the scanner must
// leave unflagged.
func SafeInMemory() bootstrap.EventTransportKind {
	return bootstrap.InMemoryEventTransport()
}
