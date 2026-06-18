//go:build archtest_fixture

// Package relayctorfixture contains intentionally-violating call sites against
// the outbox relay constructor + registrar banned IN cellmodules/ by
// RELAY-CONSTRUCTION-CELLMODULE-BAN-01 (see
// relay_construction_cellmodule_ban_test.go).
//
// Gated by the archtest_fixture build tag; production builds never see this
// file. Loaded by TestRelayConstructionCellmoduleBan_RedFixtureDetected via
// Run(t, archtest.Fixture(...)) (which injects the archtest_fixture tag) and run
// through the SAME scanRelayConstructionViolations the production dogfood uses
// (single source). It proves the scanner is non-vacuous: it really flags
// runtime/outbox.NewRelay and runtime/bootstrap.WithRelay calls.
//
// # Forms covered (2 hits)
//
//   - runtime/outbox.NewRelay      (relay construction)
//   - runtime/bootstrap.WithRelay  (relay registration)
//
// nil interface args compile fine — the scanner resolves the callee package +
// name via go/types, it does not execute the call.
package relayctorfixture

import (
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	outboxruntime "github.com/ghbvf/gocell/framework/runtime/outbox"
)

// newRelayCall exercises the banned relay constructor (1 hit).
func newRelayCall() {
	_ = outboxruntime.NewRelay(nil, nil, nil, outboxruntime.RelayConfig{})
}

// withRelayCall exercises the banned relay registrar (1 hit).
func withRelayCall() {
	_ = bootstrap.WithRelay(bootstrap.DefaultInstanceKey(), nil)
}
