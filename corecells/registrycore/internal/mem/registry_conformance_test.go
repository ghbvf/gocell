package mem

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports/conformance"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
)

// TestMemRegistry_Conformance enrolls the in-memory Registry in the shared
// ports.Registry contract suite (REGISTRY-CONFORMANCE-ENROLLMENT-01). The mem
// store needs no ambient tx, so the factory pairs it with the cell-boundary
// pass-through outbox.DemoTxRunner the suite wraps writes in.
func TestMemRegistry_Conformance(t *testing.T) {
	conformance.RunRegistryConformance(t, memRegistryFactory)
}

func memRegistryFactory(t *testing.T) (ports.Registry, persistence.TxRunner, func()) {
	t.Helper()
	return NewRegistry(clock.Real()), outbox.DemoTxRunner{}, func() {}
}
