package mem_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain/conformance"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// TestMemDeviceRepository_Conformance enrolls the in-memory DeviceRepository
// against the shared behavioral suite defined in conformance.RunDeviceRepoConformance.
// RequiresAmbientTx=false because mem serializes via mutex — no real tx concept.
func TestMemDeviceRepository_Conformance(t *testing.T) {
	factory := func(t *testing.T) (domain.DeviceRepository, persistence.TxRunner, func() time.Time, func()) {
		t.Helper()
		repo := mem.NewDeviceRepository()
		return repo, noopTxRunner{}, time.Now, func() {}
	}
	conformance.RunDeviceRepoConformance(t, factory, conformance.Features{RequiresAmbientTx: false})
}

// TestMemDeviceRepository_RepoReadinessConformance wires the in-memory
// DeviceRepository through the shared RepoHealthProber conformance harness.
// The mem implementation always returns nil from RepoReady (no real schema to
// break), so the broken prober is passed as nil — the harness records the
// schema-broken sub-test as skipped.
func TestMemDeviceRepository_RepoReadinessConformance(t *testing.T) {
	celltest.RunRepoReadinessConformance(t, "devicecell-mem", mem.NewDeviceRepository(), nil)
}

// noopTxRunner satisfies persistence.TxRunner for in-memory implementations
// that do not require real transactions. fn is called directly in the same
// goroutine — no commit/rollback semantics.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
