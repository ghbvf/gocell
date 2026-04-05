//go:build integration

package integration

import "testing"

// ---------------------------------------------------------------------------
// Assembly combination tests (T74)
// ---------------------------------------------------------------------------

// TestAssembly_CoreBundleStartStop verifies that the core-bundle assembly
// (access-core + audit-core + config-core) starts and stops cleanly.
func TestAssembly_CoreBundleStartStop(t *testing.T) {
	t.Skip("stub: requires full Cell implementations registered in core-bundle")
}

// TestAssembly_CoreBundleHealth verifies that all cells in core-bundle
// report healthy status after startup.
func TestAssembly_CoreBundleHealth(t *testing.T) {
	t.Skip("stub: requires full Cell implementations registered in core-bundle")
}

// TestAssembly_StartFailureRollback verifies that if one cell fails to start,
// all previously started cells are stopped in reverse order.
func TestAssembly_StartFailureRollback(t *testing.T) {
	t.Skip("stub: requires injectable fault cell for testing rollback")
}

// TestAssembly_GracefulShutdown verifies that Stop drains in-flight work
// (outbox poller, event consumers) before returning.
func TestAssembly_GracefulShutdown(t *testing.T) {
	t.Skip("stub: requires full Cell implementations with active workers")
}

// TestAssembly_ContractWiring verifies that inter-cell contracts are correctly
// wired: access-core produces session.created, audit-core subscribes to it.
func TestAssembly_ContractWiring(t *testing.T) {
	t.Skip("stub: requires contract registry with runtime wiring")
}
