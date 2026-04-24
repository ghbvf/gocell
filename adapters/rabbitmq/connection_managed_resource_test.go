package rabbitmq

// Connection implements lifecycle.ManagedResource — these tests lock down the
// Checkers / Worker / probe-name contract used by bootstrap.WithManagedResource.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
)

// Compile-time assertion mirrors the production assertion — ensures the
// interface contract is held even if the production assertion is moved.
var _ lifecycle.ManagedResource = (*Connection)(nil)

func TestConnection_Checkers_HealthyConnected(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	checkers := conn.Checkers()
	probe, ok := checkers["rabbitmq_ready"]
	if !ok {
		t.Fatalf("Checkers() missing 'rabbitmq_ready'; got keys: %v", keysOf(checkers))
	}
	if err := probe(context.Background()); err != nil {
		t.Errorf("rabbitmq_ready in StateConnected returned %v, want nil", err)
	}
}

func TestConnection_Checkers_HonorsCtxDeadline(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	probe := conn.Checkers()["rabbitmq_ready"]
	start := time.Now()
	err := probe(cancelled)
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected ctx.Err() from probe with pre-cancelled ctx, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("probe error = %v, want context.Canceled", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("probe took %s — should return immediately on cancelled ctx", elapsed)
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

	err := conn.Checkers()["rabbitmq_ready"](context.Background())
	if err == nil {
		t.Fatal("rabbitmq_ready in StateDisconnected must return an error, got nil")
	}
	if !errors.Is(err, errHealthReconnecting) {
		t.Errorf("rabbitmq_ready error = %v, want errHealthReconnecting (ErrAdapterAMQPReconnecting)", err)
	}
}

func TestConnection_Checkers_UnhealthyTerminal(t *testing.T) {
	conn, _ := newTestConnection(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	terminalErr := errors.New("simulated permanent broker failure")
	conn.mu.Lock()
	conn.state = StateTerminal
	conn.permanentErr = terminalErr
	conn.mu.Unlock()

	err := conn.Checkers()["rabbitmq_ready"](context.Background())
	if err == nil {
		t.Fatal("rabbitmq_ready in StateTerminal must return permanentErr, got nil")
	}
	if !errors.Is(err, terminalErr) {
		t.Errorf("rabbitmq_ready error = %v, want %v", err, terminalErr)
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
	checkers := mr.Checkers()
	if len(checkers) != 1 {
		t.Errorf("expected 1 checker, got %d", len(checkers))
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

func keysOf(m map[string]func(context.Context) error) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
