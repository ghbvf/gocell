package lifecycle_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
)

// fakeProbeName is a test-only sanctioned ProbeName const. The ManagedResource
// contract requires that callers funnel through a typed ProbeName; the
// constructor route (MustProbeName) is the test-side entry point that
// archtest A4b allowlists for *_test.go.
var fakeProbeName = healthz.MustProbeName("fake_ready")

type fakeResource struct{}

func (fakeResource) Probes() []healthz.Probe {
	return []healthz.Probe{
		healthz.NewProbe(fakeProbeName, func(_ context.Context) error { return nil }),
	}
}
func (fakeResource) Worker() worker.Worker         { return nil }
func (fakeResource) Close(_ context.Context) error { return nil }

var _ lifecycle.ManagedResource = fakeResource{}

func TestManagedResource_InterfaceContract(t *testing.T) {
	var r lifecycle.ManagedResource = fakeResource{}
	if len(r.Probes()) != 1 {
		t.Errorf("expected 1 probe, got %d", len(r.Probes()))
	}
	if got := r.Probes()[0].Name(); got != fakeProbeName {
		t.Errorf("probe name = %q, want %q", got, fakeProbeName)
	}
	if err := r.Probes()[0].Check(context.Background()); err != nil {
		t.Errorf("probe Check returned err: %v", err)
	}
	if r.Worker() != nil {
		t.Errorf("expected nil worker")
	}
	if err := r.Close(context.Background()); err != nil {
		t.Errorf("unexpected Close error: %v", err)
	}
}
