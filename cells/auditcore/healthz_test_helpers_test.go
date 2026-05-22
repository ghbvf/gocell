package auditcore

import (
	"context"

	"github.com/ghbvf/gocell/kernel/healthz"
)

// testAggregator is a test-local healthz.Aggregator stub that records
// registered probes by name.
type testAggregator struct {
	probes map[string]healthz.Probe
}

func newTestAgg() *testAggregator {
	return &testAggregator{probes: make(map[string]healthz.Probe)}
}

func (a *testAggregator) Register(p healthz.Probe) error {
	if _, dup := a.probes[p.Name()]; dup {
		return healthz.ErrDuplicateProbe
	}
	a.probes[p.Name()] = p
	return nil
}

func (a *testAggregator) Deregister(name string) { delete(a.probes, name) }

func (a *testAggregator) Evaluate(_ context.Context) healthz.Snapshot {
	return healthz.Snapshot{}
}
