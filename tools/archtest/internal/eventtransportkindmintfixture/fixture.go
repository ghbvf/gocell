//go:build archtest_fixture

// Package eventtransportkindmintfixture is the RED fixture for
// EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01.
//
// It mints the real-broker EventTransportKind via bootstrap.RealBrokerEventTransport()
// from OUTSIDE cellmodules/eventtransport — the sole sanctioned minter. That is
// the forge the funnel must flag: a composition root (or any package) that hands
// the phase0 split-topology gate a "real broker" claim without going through
// eventtransport.Resolve, which mints it only when it actually constructs the
// RabbitMQ transport (#2211).
//
// InMemoryEventTransport() is the GREEN control: it is the safe direction (a
// split topology carrying it is rejected by the gate), so the scanner must NOT
// flag it.
//
// DO NOT use this package in production code.
package eventtransportkindmintfixture

import "github.com/ghbvf/gocell/framework/runtime/bootstrap"

// ForgeRealBroker mints the real-broker kind outside eventtransport — the
// violation the funnel must catch.
func ForgeRealBroker() bootstrap.EventTransportKind {
	return bootstrap.RealBrokerEventTransport()
}

// SafeInMemory mints the in-memory kind — the GREEN control the scanner must
// leave unflagged.
func SafeInMemory() bootstrap.EventTransportKind {
	return bootstrap.InMemoryEventTransport()
}
