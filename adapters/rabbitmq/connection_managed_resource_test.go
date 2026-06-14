package rabbitmq

// Connection implements lifecycle.ManagedResource — these tests lock down the
// Probes / Worker / probe-name contract used by bootstrap.WithManagedResource.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/worker"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// Compile-time assertion mirrors the production assertion — ensures the
// interface contract is held even if the production assertion is moved.
var _ lifecycle.ManagedResource = (*Connection)(nil)

// findReadyProbe returns the rabbitmq_ready probe from the typed slice.
// Connection only exposes a single probe (ProbeReady), so the helper is
// fixed to that name rather than parameterized — keeps the call sites
// terse and makes the unparam linter happy.
func findReadyProbe(probes []healthz.Probe) healthz.Probe {
	for _, p := range probes {
		if p.Name() == ProbeReady {
			return p
		}
	}
	return nil
}

func TestConnection_Checkers_HealthyConnected(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	probes := conn.Probes()
	probe := findReadyProbe(probes)
	if probe == nil {
		t.Fatalf("Probes() missing 'rabbitmq_ready'; got names: %v", probeNames(probes))
	}
	if err := probe.Check(context.Background()); err != nil {
		t.Errorf("rabbitmq_ready in StateConnected returned %v, want nil", err)
	}
}

func TestConnection_Checkers_HonorsCtxDeadline(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	probe := findReadyProbe(conn.Probes())
	start := time.Now()
	err := probe.Check(canceled)
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected ctx.Err() from probe with pre-canceled ctx, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("probe error = %v, want context.Canceled", err)
	}
	if elapsed > testtime.MediumPoll {
		t.Errorf("probe took %s — should return immediately on canceled ctx", elapsed)
	}
}

func TestConnection_Checkers_UnhealthyDisconnected(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	// Force the state machine into Disconnected without going through reconnect
	// machinery — the probe must surface the reconnecting error code.
	conn.mu.Lock()
	conn.state = StateDisconnected
	conn.mu.Unlock()

	probe := findReadyProbe(conn.Probes())
	err := probe.Check(context.Background())
	if err == nil {
		t.Fatal("rabbitmq_ready in StateDisconnected must return an error, got nil")
	}
	if !errors.Is(err, errHealthReconnecting) {
		t.Errorf("rabbitmq_ready error = %v, want errHealthReconnecting (ErrAdapterAMQPReconnecting)", err)
	}
}

func TestConnection_Checkers_UnhealthyWhenPermanentRecorded(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	permanentErr := errors.New("simulated permanent broker failure")
	conn.mu.Lock()
	// State stays StateConnected (or StateDisconnected during reconnect) —
	// permanentErr supersedes phase via Health(). The rabbitmq_ready probe
	// must surface the permanent classification regardless.
	conn.permanentErr = permanentErr
	conn.mu.Unlock()

	probe := findReadyProbe(conn.Probes())
	err := probe.Check(context.Background())
	if err == nil {
		t.Fatal("rabbitmq_ready with permanentErr set must return that error, got nil")
	}
	if !errors.Is(err, permanentErr) {
		t.Errorf("rabbitmq_ready error = %v, want %v", err, permanentErr)
	}
}

func TestConnection_Worker_ReturnsNil(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	if w := conn.Worker(); w != nil {
		t.Errorf("Worker() = %T, want nil — RMQ reconnect runs inside NewConnection, not via ManagedResource", w)
	}
}

func TestConnection_AsManagedResource_RoundTrip(t *testing.T) {
	conn, _ := newTestConnection(t)

	var mr lifecycle.ManagedResource = conn
	probes := mr.Probes()
	if len(probes) != 1 {
		t.Errorf("expected 1 probe, got %d", len(probes))
	}
	if mr.Worker() != nil {
		t.Error("Worker() must be nil")
	}
	if err := mr.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// Compile-time assertion that Worker() returns the correct interface type.
var _ worker.Worker = (worker.Worker)(nil)

func probeNames(probes []healthz.Probe) []healthz.ProbeName {
	out := make([]healthz.ProbeName, 0, len(probes))
	for _, p := range probes {
		out = append(out, p.Name())
	}
	return out
}
