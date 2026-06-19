//go:build integration

package postgres

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports/conformance"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
)

// TestPGRegistry_Conformance enrolls the PostgreSQL Registry in the shared
// ports.Registry contract suite (REGISTRY-CONFORMANCE-ENROLLMENT-01). Each
// sub-test gets a fresh per-test database clone via setupRegistryPG; the returned
// TxManager provides the ambient tx the suite wraps writes in (PG reads isolate
// via the explicit tenant predicate, mirroring the existing integration tests).
func TestPGRegistry_Conformance(t *testing.T) {
	conformance.RunRegistryConformance(t, pgRegistryFactory)
}

func pgRegistryFactory(t *testing.T) (ports.Registry, persistence.TxRunner, func()) {
	t.Helper()
	repo, txMgr := setupRegistryPG(t)
	// No-op cleanup: setupRegistryPG registers the per-test database teardown via
	// t.Cleanup, so the factory's cleanup func has nothing left to release.
	return repo, txMgr, func() {}
}
