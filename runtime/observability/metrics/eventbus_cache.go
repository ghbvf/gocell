package metrics

import (
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// EventbusCacheCollector records eventbus subscriber-cache lifecycle metrics.
type EventbusCacheCollector interface {
	RecordTombstoneEvicted(cellID, sliceID string)
}

// NoopEventbusCacheCollector drops eventbus cache observations.
type NoopEventbusCacheCollector struct{}

func (NoopEventbusCacheCollector) RecordTombstoneEvicted(string, string) {
	// Intentionally empty: callers can inject this collector when eventbus-cache
	// metrics are disabled while keeping service code free of nil checks.
}

type providerEventbusCacheCollector struct {
	tombstoneEvicted kernelmetrics.CounterVec
}

var _ EventbusCacheCollector = (*providerEventbusCacheCollector)(nil)

// NewProviderEventbusCacheCollector registers eventbus-cache metrics on p.
// The Prometheus provider namespace supplies the "gocell_" fqName prefix.
func NewProviderEventbusCacheCollector(p kernelmetrics.Provider) (EventbusCacheCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: eventbus cache Provider is required")
	}
	tombstoneEvicted, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "eventbus_cache_tombstone_evicted_total",
		Help:       "Total number of subscriber-cache tombstone entries evicted by TTL garbage collection, partitioned by owning cell and slice.",
		LabelNames: []string{"cell", "slice"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register eventbus_cache_tombstone_evicted_total: %w", err)
	}
	return &providerEventbusCacheCollector{tombstoneEvicted: tombstoneEvicted}, nil
}

func (c *providerEventbusCacheCollector) RecordTombstoneEvicted(cellID, sliceID string) {
	if c == nil {
		return
	}
	c.tombstoneEvicted.With(kernelmetrics.Labels{"cell": cellID, "slice": sliceID}).Inc()
}
