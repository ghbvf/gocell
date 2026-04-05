//go:build integration

// Package integration_test contains end-to-end journey and assembly tests.
package integration_test

import "testing"

// ---------------------------------------------------------------------------
// Assembly: core-bundle (all 3 core cells)
// ---------------------------------------------------------------------------

// TestAssembly_CoreBundleStartStop verifies that the core-bundle assembly
// (access-core + audit-core + config-core) starts and stops cleanly.
func TestAssembly_CoreBundleStartStop(t *testing.T) {
	t.Skip("requires Docker: full assembly with postgres + redis + rabbitmq")
	// Steps:
	// 1. Start postgres, redis, rabbitmq containers
	// 2. Create core-bundle assembly with all 3 cells
	// 3. Verify assembly.Start() completes without error
	// 4. Verify all cells report healthy
	// 5. Verify assembly.Stop() completes without error (LIFO order)
}

// TestAssembly_CoreBundleHealth verifies aggregated health reporting
// across all cells in the core-bundle.
func TestAssembly_CoreBundleHealth(t *testing.T) {
	t.Skip("requires Docker: health check with degraded adapter")
	// Steps:
	// 1. Start core-bundle assembly
	// 2. GET /internal/v1/health — verify all cells healthy
	// 3. Kill redis container (simulate failure)
	// 4. GET /internal/v1/health — verify access-core reports degraded
	// 5. Restart redis container
	// 6. GET /internal/v1/health — verify recovery to healthy
}

// TestAssembly_CoreBundlePartialFailure verifies assembly behavior when
// one cell fails during startup (rollback semantics).
func TestAssembly_CoreBundlePartialFailure(t *testing.T) {
	t.Skip("requires Docker: assembly start with one cell failing init")
	// Steps:
	// 1. Start postgres + redis (no rabbitmq — audit-core depends on it)
	// 2. Create core-bundle assembly
	// 3. Verify assembly.Start() returns error for audit-core
	// 4. Verify access-core and config-core are rolled back (stopped)
	// 5. Verify assembly state is stopped (can retry)
}

// TestAssembly_CoreBundleContractWiring verifies that inter-cell contracts
// are properly wired at assembly startup.
func TestAssembly_CoreBundleContractWiring(t *testing.T) {
	t.Skip("requires Docker: verify contract bindings between cells")
	// Steps:
	// 1. Start core-bundle assembly
	// 2. Verify access-core produces session-events contract
	// 3. Verify audit-core consumes session-events contract
	// 4. Verify config-core produces config-events contract
	// 5. Publish test event — verify consumer receives it
}

// TestAssembly_CoreBundleGracefulShutdown verifies that in-flight requests
// are drained during shutdown.
func TestAssembly_CoreBundleGracefulShutdown(t *testing.T) {
	t.Skip("requires Docker: graceful shutdown with in-flight requests")
	// Steps:
	// 1. Start core-bundle assembly
	// 2. Start a slow request (e.g., long-running query)
	// 3. Trigger assembly.Stop()
	// 4. Verify slow request completes before shutdown
	// 5. Verify new requests are rejected after shutdown signal
}
