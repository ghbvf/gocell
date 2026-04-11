package eventbus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishSubscribe(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var received []outbox.Entry
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "test.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			mu.Lock()
			received = append(received, e)
			mu.Unlock()
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Give subscriber time to register.
	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "test.topic", []byte(`{"key":"value"}`))
	require.NoError(t, err)

	err = bus.Publish(context.Background(), "test.topic", []byte(`{"key":"value2"}`))
	require.NoError(t, err)

	// Wait for processing.
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 2
	}, time.Second, 10*time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	assert.Len(t, received, 2)
	assert.Equal(t, []byte(`{"key":"value"}`), received[0].Payload)
	assert.Equal(t, []byte(`{"key":"value2"}`), received[1].Payload)
	mu.Unlock()
}

func TestPublish_NoSubscribers(t *testing.T) {
	bus := New()
	defer func() { _ = bus.Close() }()

	err := bus.Publish(context.Background(), "no.subs", []byte("data"))
	assert.NoError(t, err)
}

func TestSubscribe_RetryAndDeadLetter(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var attempts atomic.Int32
	testErr := errors.New("transient error")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "retry.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			attempts.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: testErr}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "retry.topic", []byte("fail"))
	require.NoError(t, err)

	// Wait for all retries to complete (3 attempts with delays: 100+200+400 = 700ms).
	assert.Eventually(t, func() bool {
		return attempts.Load() >= 3
	}, 3*time.Second, 50*time.Millisecond)

	// Message should be in dead letter.
	assert.Eventually(t, func() bool {
		return bus.DeadLetterLen() == 1
	}, time.Second, 50*time.Millisecond)

	dl := bus.DrainDeadLetters()
	require.Len(t, dl, 1)
	assert.Equal(t, "retry.topic", dl[0].Topic)
	assert.Equal(t, testErr, dl[0].LastErr)

	// After drain, dead letter should be empty.
	assert.Equal(t, 0, bus.DeadLetterLen())

	cancel()
	<-done
}

func TestSubscribe_RejectGoesDirectlyToDeadLetter(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var attempts atomic.Int32
	testErr := errors.New("permanent error")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "reject.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			attempts.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionReject, Err: testErr}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "reject.topic", []byte("perm-fail"))
	require.NoError(t, err)

	// Should go directly to dead letter on first attempt (no retries).
	assert.Eventually(t, func() bool {
		return bus.DeadLetterLen() == 1
	}, time.Second, 50*time.Millisecond)

	assert.Equal(t, int32(1), attempts.Load(), "reject should not trigger retries")

	dl := bus.DrainDeadLetters()
	require.Len(t, dl, 1)
	assert.Equal(t, testErr, dl[0].LastErr)

	cancel()
	<-done
}

func TestClose_PreventsFurtherPublish(t *testing.T) {
	bus := New()
	err := bus.Close()
	require.NoError(t, err)

	err = bus.Publish(context.Background(), "topic", []byte("data"))
	assert.Error(t, err)
}

func TestClose_Idempotent(t *testing.T) {
	bus := New()
	assert.NoError(t, bus.Close())
	assert.NoError(t, bus.Close())
}

func TestSubscribe_ClosedBus(t *testing.T) {
	bus := New()
	_ = bus.Close()

	err := bus.Subscribe(context.Background(), "topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	assert.Error(t, err)
}

func TestMultipleSubscribers(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var count1, count2 atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, "multi.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			count1.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, "multi.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			count2.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "multi.topic", []byte("hello"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return count1.Load() == 1 && count2.Load() == 1
	}, time.Second, 10*time.Millisecond)

	cancel()
	wg.Wait()
}

func TestSubscribe_SuccessAfterRetry(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var attempts atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "partial.fail", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			n := attempts.Add(1)
			if n < 3 {
				return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("not yet")}
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "partial.fail", []byte("data"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return attempts.Load() >= 3
	}, 3*time.Second, 50*time.Millisecond)

	// Should NOT be in dead letter (succeeded on 3rd attempt).
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, bus.DeadLetterLen())

	cancel()
	<-done
}

func TestHealth(t *testing.T) {
	bus := New()
	assert.Equal(t, "healthy", bus.Health())

	_ = bus.Close()
	assert.Equal(t, "closed", bus.Health())
}

func TestTopicConfigChangedConstant(t *testing.T) {
	assert.Equal(t, "event.config.changed.v1", TopicConfigChanged)
}

func TestSubscribe_CleansUpOnExit(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "cleanup.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Wait for subscriber to register.
	time.Sleep(20 * time.Millisecond)

	bus.mu.RLock()
	subsBefore := len(bus.subs["cleanup.topic"])
	bus.mu.RUnlock()
	assert.Equal(t, 1, subsBefore, "subscriber should be registered")

	// Cancel the subscriber.
	cancel()
	<-done

	// After exit, the subscription should be removed.
	bus.mu.RLock()
	subsAfter := len(bus.subs["cleanup.topic"])
	bus.mu.RUnlock()
	assert.Equal(t, 0, subsAfter, "subscriber should be cleaned up after exit")
}

// Verify interface compliance at compile time.
var (
	_ outbox.Publisher  = (*InMemoryEventBus)(nil)
	_ outbox.Subscriber = (*InMemoryEventBus)(nil)
)
