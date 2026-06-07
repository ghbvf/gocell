package grpc_test

import (
	"context"
	"sync"
	"testing"

	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
)

func TestDrainSignal_FreshNotCancelled(t *testing.T) {
	d := runtimegrpc.NewDrainSignal()
	if d.Context() == nil {
		t.Fatalf("NewDrainSignal().Context() must not be nil")
	}
	if err := d.Context().Err(); err != nil {
		t.Fatalf("fresh DrainSignal context must not be canceled, got %v", err)
	}
}

func TestDrainSignal_TriggerCancelsContext(t *testing.T) {
	d := runtimegrpc.NewDrainSignal()
	d.Trigger()
	select {
	case <-d.Context().Done():
	default:
		t.Fatalf("Context().Done() must be closed after Trigger")
	}
	if err := d.Context().Err(); err != context.Canceled {
		t.Fatalf("after Trigger, Context().Err() = %v, want context.Canceled", err)
	}
}

func TestDrainSignal_TriggerIdempotent(t *testing.T) {
	d := runtimegrpc.NewDrainSignal()
	d.Trigger()
	d.Trigger() // second call must be a harmless no-op (no panic)
	if err := d.Context().Err(); err != context.Canceled {
		t.Fatalf("Context().Err() = %v, want context.Canceled", err)
	}
}

// TestDrainSignal_TriggerConcurrent asserts concurrent Trigger() is race-free —
// GracefulStop may be reached from multiple goroutines. Run with -race.
func TestDrainSignal_TriggerConcurrent(t *testing.T) {
	d := runtimegrpc.NewDrainSignal()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.Trigger() }()
	}
	wg.Wait()
	if err := d.Context().Err(); err != context.Canceled {
		t.Fatalf("Context().Err() = %v, want context.Canceled after concurrent triggers", err)
	}
}
