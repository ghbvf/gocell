package eventbus

import (
	"context"
	"errors"
	"fmt"
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

func TestSubscribe_PermanentErrorInRequeue_RoutesToDeadLetter(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var attempts atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "perm.requeue", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			attempts.Add(1)
			// Return Requeue with PermanentError — eventbus should detect
			// the PermanentError and route directly to dead letter.
			return outbox.HandleResult{
				Disposition: outbox.DispositionRequeue,
				Err:         outbox.NewPermanentError(errors.New("unmarshal failed")),
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "perm.requeue", []byte("bad-payload"))
	require.NoError(t, err)

	// Should go directly to dead letter on first attempt (no retries).
	assert.Eventually(t, func() bool {
		return bus.DeadLetterLen() == 1
	}, time.Second, 50*time.Millisecond)

	assert.Equal(t, int32(1), attempts.Load(),
		"PermanentError in Requeue must not trigger retries")

	dl := bus.DrainDeadLetters()
	require.Len(t, dl, 1)

	var permErr *outbox.PermanentError
	assert.True(t, errors.As(dl[0].LastErr, &permErr))

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

func TestClose_ConcurrentPublishDoesNotPanic(t *testing.T) {
	bus := New(WithBufferSize(32))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "race.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	require.Eventually(t, func() bool {
		bus.mu.RLock()
		defer bus.mu.RUnlock()
		return len(bus.subs["race.topic"]) == 1
	}, time.Second, 10*time.Millisecond)

	var stop atomic.Bool
	panicCh := make(chan any, 1)
	var publishers sync.WaitGroup
	for i := 0; i < 4; i++ {
		publishers.Add(1)
		go func(workerID int) {
			defer publishers.Done()
			defer func() {
				if r := recover(); r != nil {
					select {
					case panicCh <- fmt.Sprintf("publisher %d panicked: %v", workerID, r):
					default:
					}
				}
			}()

			for !stop.Load() {
				_ = bus.Publish(context.Background(), "race.topic", []byte("payload"))
			}
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, bus.Close())
	stop.Store(true)
	publishers.Wait()
	cancel()
	<-done

	select {
	case panicValue := <-panicCh:
		t.Fatalf("publish panicked during concurrent close: %v", panicValue)
	default:
	}
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

// mockReceipt records Commit/Release calls for testing.
type mockReceipt struct {
	committed atomic.Bool
	released  atomic.Bool
}

func (r *mockReceipt) Commit(_ context.Context) error {
	r.committed.Store(true)
	return nil
}

func (r *mockReceipt) Release(_ context.Context) error {
	r.released.Store(true)
	return nil
}

func TestSubscribe_ReceiptCommittedOnAck(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	receipt := &mockReceipt{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "receipt.ack", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{
				Disposition: outbox.DispositionAck,
				Receipt:     receipt,
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "receipt.ack", []byte("data"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return receipt.committed.Load()
	}, time.Second, 10*time.Millisecond, "receipt should be committed on Ack")

	assert.False(t, receipt.released.Load(), "receipt should not be released on Ack")

	cancel()
	<-done
}

func TestSubscribe_ReceiptReleasedOnReject(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	receipt := &mockReceipt{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "receipt.reject", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{
				Disposition: outbox.DispositionReject,
				Err:         errors.New("permanent"),
				Receipt:     receipt,
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "receipt.reject", []byte("data"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return receipt.released.Load()
	}, time.Second, 10*time.Millisecond, "receipt should be released on Reject")

	assert.False(t, receipt.committed.Load(), "receipt should not be committed on Reject")

	cancel()
	<-done
}

func TestSubscribe_ReceiptReleasedOnRequeue(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var receipts []*mockReceipt
	var receiptsMu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "receipt.requeue", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			r := &mockReceipt{}
			receiptsMu.Lock()
			receipts = append(receipts, r)
			receiptsMu.Unlock()
			return outbox.HandleResult{
				Disposition: outbox.DispositionRequeue,
				Err:         errors.New("transient"),
				Receipt:     r,
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "receipt.requeue", []byte("data"))
	require.NoError(t, err)

	// Wait for all retries to exhaust.
	assert.Eventually(t, func() bool {
		return bus.DeadLetterLen() == 1
	}, 5*time.Second, 50*time.Millisecond)

	receiptsMu.Lock()
	defer receiptsMu.Unlock()

	require.Len(t, receipts, maxRetries, "should have one receipt per retry attempt")

	for i, r := range receipts {
		assert.True(t, r.released.Load(), "receipt %d should be released on Requeue", i)
		assert.False(t, r.committed.Load(), "receipt %d should not be committed on Requeue", i)
	}

	cancel()
	<-done
}

func TestSubscribe_ReceiptReleasedOnRetryExhaustion(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	// Track all receipts across retry attempts to verify each is released
	// and none is committed when retries exhaust.
	var receipts []*mockReceipt
	var receiptsMu sync.Mutex

	testErr := errors.New("persistent transient error")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "receipt.exhaust", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			r := &mockReceipt{}
			receiptsMu.Lock()
			receipts = append(receipts, r)
			receiptsMu.Unlock()
			return outbox.HandleResult{
				Disposition: outbox.DispositionRequeue,
				Err:         testErr,
				Receipt:     r,
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "receipt.exhaust", []byte("exhaust-data"))
	require.NoError(t, err)

	// Wait for retries to exhaust and message to land in dead letter.
	assert.Eventually(t, func() bool {
		return bus.DeadLetterLen() == 1
	}, 5*time.Second, 50*time.Millisecond)

	receiptsMu.Lock()
	defer receiptsMu.Unlock()

	require.Equal(t, maxRetries, len(receipts), "handler should be called exactly maxRetries times")

	for i, r := range receipts {
		assert.True(t, r.released.Load(), "receipt %d should be released after requeue", i)
		assert.False(t, r.committed.Load(), "receipt %d must never be committed on retry exhaustion", i)
	}

	// Verify dead letter contains the correct error.
	dl := bus.DrainDeadLetters()
	require.Len(t, dl, 1)
	assert.Equal(t, testErr, dl[0].LastErr)
	assert.Equal(t, "receipt.exhaust", dl[0].Topic)

	cancel()
	<-done
}

func TestSubscribe_ZeroValueDisposition_TreatedAsRequeue(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var attempts atomic.Int32
	var receipts []*mockReceipt
	var receiptsMu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "zero.disp", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			attempts.Add(1)
			r := &mockReceipt{}
			receiptsMu.Lock()
			receipts = append(receipts, r)
			receiptsMu.Unlock()
			// Zero-value HandleResult — Disposition is 0 (invalid).
			return outbox.HandleResult{
				Err:     errors.New("forgot disposition"),
				Receipt: r,
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	err := bus.Publish(context.Background(), "zero.disp", []byte("data"))
	require.NoError(t, err)

	// Should exhaust retries and land in dead letter.
	assert.Eventually(t, func() bool {
		return bus.DeadLetterLen() == 1
	}, 5*time.Second, 50*time.Millisecond)

	elapsed := time.Since(start)

	// Must have retried exactly maxRetries times.
	assert.Equal(t, int32(maxRetries), attempts.Load(),
		"zero-value disposition should retry exactly maxRetries times")

	// Must have applied backoff (at least ~300ms for 3 attempts: 100+200ms minimum).
	assert.GreaterOrEqual(t, elapsed.Milliseconds(), int64(250),
		"zero-value disposition must apply backoff like DispositionRequeue")

	// All receipts must be released, none committed.
	receiptsMu.Lock()
	defer receiptsMu.Unlock()
	require.Len(t, receipts, maxRetries)
	for i, r := range receipts {
		assert.True(t, r.released.Load(), "receipt %d should be released", i)
		assert.False(t, r.committed.Load(), "receipt %d should not be committed", i)
	}

	// Dead letter should contain the error.
	dl := bus.DrainDeadLetters()
	require.Len(t, dl, 1)
	assert.Equal(t, "zero.disp", dl[0].Topic)

	cancel()
	<-done
}

func TestSubscribe_UnknownDisposition_TreatedAsRequeue(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var attempts atomic.Int32
	testErr := errors.New("unknown disp error")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "unknown.disp", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			attempts.Add(1)
			return outbox.HandleResult{
				Disposition: outbox.Disposition(99), // not a valid Disposition
				Err:         testErr,
			}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	err := bus.Publish(context.Background(), "unknown.disp", []byte("data"))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return bus.DeadLetterLen() == 1
	}, 5*time.Second, 50*time.Millisecond)

	elapsed := time.Since(start)

	assert.Equal(t, int32(maxRetries), attempts.Load(),
		"unknown disposition should retry exactly maxRetries times")
	assert.GreaterOrEqual(t, elapsed.Milliseconds(), int64(250),
		"unknown disposition must apply backoff")

	dl := bus.DrainDeadLetters()
	require.Len(t, dl, 1)
	assert.Equal(t, testErr, dl[0].LastErr)

	cancel()
	<-done
}

func TestSubscribe_InvalidDisposition_RespectsCtxCancel(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var attempts atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, "cancel.disp", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			n := attempts.Add(1)
			if n == 1 {
				// After first attempt with invalid disposition, cancel ctx
				// during backoff to verify early exit.
				go func() {
					time.Sleep(10 * time.Millisecond)
					cancel()
				}()
			}
			return outbox.HandleResult{} // zero-value
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := bus.Publish(context.Background(), "cancel.disp", []byte("data"))
	require.NoError(t, err)

	// Subscribe should return promptly after cancel.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not exit after ctx cancel during invalid disposition backoff")
	}

	// Should have been called only once — cancelled during backoff before retry.
	assert.Equal(t, int32(1), attempts.Load(),
		"should exit during backoff, not retry after cancel")
}

// Verify interface compliance at compile time.
var (
	_ outbox.Publisher  = (*InMemoryEventBus)(nil)
	_ outbox.Subscriber = (*InMemoryEventBus)(nil)
)
