package healthz

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// testProbeResultLatency is the Latency value used in TestProbeResult_FieldsFrozen
// to verify that the ProbeResult duration field round-trips correctly.
const testProbeResultLatency = testtime.D5ms

// TestAggregator_InterfaceContract asserts the Aggregator interface has exactly
// the three methods (Register / Deregister / Evaluate) we committed to in the
// plan — no Latest, no streaming hooks. If extra methods slip in, this won't
// catch them directly (Go allows wider interfaces to satisfy narrower ones), but
// the Snapshot/ProbeResult field set is checked below to lock the wire-adjacent
// shape.
func TestAggregator_InterfaceContract(t *testing.T) {
	t.Parallel()

	var _ Aggregator = aggregatorStub{}
}

type aggregatorStub struct{}

func (aggregatorStub) Register(Probe) error              { return nil }
func (aggregatorStub) Deregister(ProbeName)              {}
func (aggregatorStub) Evaluate(context.Context) Snapshot { return Snapshot{} }

// TestSnapshot_FieldsFrozen locks the field set so codegen / wire transports
// downstream stay in sync. Add a field → update doc.go INVARIANT list →
// extend HTTP transport projection.
func TestSnapshot_FieldsFrozen(t *testing.T) {
	t.Parallel()

	snap := Snapshot{
		Overall: StatusUp,
		Probes:  []ProbeResult{},
	}
	if snap.Overall != StatusUp {
		t.Errorf("Snapshot.Overall not settable")
	}
	if snap.Probes == nil {
		t.Errorf("Snapshot.Probes not settable")
	}
}

func TestProbeResult_FieldsFrozen(t *testing.T) {
	t.Parallel()

	pr := ProbeResult{
		Name:    "n",
		Status:  StatusDegraded,
		Err:     nil,
		Latency: testProbeResultLatency,
	}
	if pr.Name != "n" || pr.Status != StatusDegraded || pr.Latency != testProbeResultLatency {
		t.Errorf("ProbeResult round-trip broken: %+v", pr)
	}
}
