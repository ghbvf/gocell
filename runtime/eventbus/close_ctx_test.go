package eventbus

// T11-T12: InMemoryEventBus.Close(_ context.Context) tests.
//
// Close intentionally ignores the ctx because closing in-memory channels is
// O(1) and must always complete unconditionally — intercepting ctx would risk
// leaving subscriber goroutines permanently blocked.
//
// ref: kernel/lifecycle doc.go — "resources that must complete teardown
// unconditionally should ignore the ctx and document the reason"

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInMemoryEventBus_Close_AcceptsCtx verifies that Close accepts a ctx
// parameter and returns nil (including with a cancelled ctx, since the
// in-memory teardown is unconditional O(1)).
func TestInMemoryEventBus_Close_AcceptsCtx(t *testing.T) {
	bus := New()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// Close must succeed even with a pre-cancelled ctx — teardown is O(1)
	// and unconditional for in-memory channels.
	err := bus.Close(cancelledCtx)
	assert.NoError(t, err, "Close with cancelled ctx must return nil for in-memory bus")
}

// TestInMemoryEventBus_Close_CancelledCtxStillClosesChannels verifies that
// even when called with a cancelled ctx, Close still terminates subscriber
// goroutines (no goroutine leak).
func TestInMemoryEventBus_Close_CancelledCtxStillClosesChannels(t *testing.T) {
	bus := New(WithBufferSize(4))
	topic := "test.close.cancelled"

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = bus.Subscribe(context.Background(), outbox.Subscription{
			Topic:         topic,
			ConsumerGroup: "cg-test",
		}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Wait for subscription to be registered.
	readyCh := bus.Ready(outbox.Subscription{Topic: topic, ConsumerGroup: "cg-test"})
	select {
	case <-readyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not become ready in time")
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := bus.Close(cancelledCtx)
	require.NoError(t, err, "Close must return nil for in-memory bus")

	// Subscriber goroutine must exit (no goroutine leak).
	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber goroutine did not exit after Close")
	}
}

// TestInMemoryEventBus_Close_ImplementsBothPublisherAndSubscriber verifies
// that *InMemoryEventBus satisfies both outbox.Publisher and outbox.Subscriber,
// both of which now require Close(ctx context.Context) error.
func TestInMemoryEventBus_Close_ImplementsBothPublisherAndSubscriber(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Close(context.Background()) }()

	var _ outbox.Publisher = bus
	var _ outbox.Subscriber = bus
}

// TestInMemoryEventBus_Close_Idempotent verifies that a second Close call
// returns nil immediately (closed flag guard).
func TestInMemoryEventBus_Close_Idempotent(t *testing.T) {
	bus := New()
	ctx := context.Background()

	assert.NoError(t, bus.Close(ctx), "first Close must return nil")
	assert.NoError(t, bus.Close(ctx), "second Close must return nil (idempotent)")
}

// TestInMemoryEventBus_Close_PreventsNewPublishes verifies that after Close,
// Publish returns an error.
func TestInMemoryEventBus_Close_PreventsNewPublishes(t *testing.T) {
	bus := New()
	require.NoError(t, bus.Close(context.Background()))

	err := bus.Publish(context.Background(), "test.topic", []byte(`{}`))
	assert.Error(t, err, "Publish after Close must return error")
}
