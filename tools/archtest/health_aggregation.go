package archtest

// health_aggregation.go — sanctioned-adapter carve-out map for HEALTH-AGG-01.
//
// The map key is anchored to PlatformModulePath so a module rename updates
// exactly one place (external.go). The scanner logic and test functions live
// in the _test.go file.

// healthAggSanctionedAdapterCarveOuts lists exported types in runtime/ or
// adapters/ that intentionally do NOT implement ManagedResource directly even
// though they expose Probes() — their Probes/Worker primitives are consumed by
// a single sanctioned adapter that owns the Close obligation.
//
// Each entry MUST cite the ADR that closes the carve-out semantically. The
// downstream Hard guard for *runtime/outbox.Relay is archtest
// RELAY-NOT-MANAGEDRESOURCE-01 (relay_isolation_test.go) — that test fails the
// moment *Relay re-satisfies ManagedResource, so this allowlist cannot widen
// in the wrong direction without an immediate second archtest failure.
var healthAggSanctionedAdapterCarveOuts = map[string]string{
	// *Relay's Probes/Worker are consumed exclusively by the
	// package-private runtime/bootstrap.relayAdapter (single sanctioned
	// holder) which owns Close → relay.Stop. Re-adding Close to *Relay
	// would regress the type isolation guarded by
	// RELAY-NOT-MANAGEDRESOURCE-01.
	PlatformModulePath + "/runtime/outbox.Relay": "docs/architecture/202605201400-adr-relay-managedresource-isolation.md",
}
