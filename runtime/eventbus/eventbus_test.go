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

// TestPublish_EnvelopePayload_UnwrappedBeforeDelivery guards the F1 fix:
// when a relay publishes an outboxMessage envelope (JSON object with id,
// eventType, payload fields), the bus must unwrap it so subscribers see the
// business payload in Entry.Payload.
//
// Regression: before the unwrap, PG-mode cells (using the in-memory bus as
// their relay publisher) delivered the envelope as-is; subscribers parsed
// envelope fields as business fields and silently ACKed unknown actions.
func TestPublish_EnvelopePayload_UnwrappedBeforeDelivery(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var got outbox.Entry
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "test.envelope.topic"},
			func(_ context.Context, e outbox.Entry) outbox.HandleResult {
				mu.Lock()
				got = e
				mu.Unlock()
				return outbox.HandleResult{Disposition: outbox.DispositionAck}
			})
	}()

	time.Sleep(20 * time.Millisecond)

	// Envelope wrapping a business payload (action/key/value), mirroring the
	// shape produced by adapters/postgres/outbox_relay.go publishAll.
	envelope := []byte(`{
		"id": "ent-123",
		"aggregateId": "agg-1",
		"aggregateType": "config",
		"eventType": "test.envelope.topic",
		"topic": "test.envelope.topic",
		"payload": {"action":"created","key":"k1","value":"v1"},
		"metadata": {"request_id":"req-42"},
		"createdAt": "2026-04-18T00:00:00Z"
	}`)
	require.NoError(t, bus.Publish(context.Background(), "test.envelope.topic", envelope))

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got.ID != ""
	}, time.Second, 10*time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	// The subscriber must see the UNWRAPPED business payload, not the envelope.
	assert.JSONEq(t, `{"action":"created","key":"k1","value":"v1"}`, string(got.Payload),
		"subscriber must receive business payload after envelope unwrap")
	// Envelope metadata fields must be preserved on the Entry for observability.
	assert.Equal(t, "ent-123", got.ID)
	assert.Equal(t, "agg-1", got.AggregateID)
	assert.Equal(t, "config", got.AggregateType)
	assert.Equal(t, "test.envelope.topic", got.EventType)
	assert.Equal(t, map[string]string{"request_id": "req-42"}, got.Metadata)
}

// TestPublish_NonEnvelopePayload_ForwardedUnchanged documents the fallback:
// direct publish paths (cells calling bus.Publish with a business payload)
// keep their pre-F1 semantics — the bus stamps an evt-{uuid} ID and forwards
// the payload byte-for-byte.
func TestPublish_NonEnvelopePayload_ForwardedUnchanged(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var got outbox.Entry
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "test.direct.topic"},
			func(_ context.Context, e outbox.Entry) outbox.HandleResult {
				mu.Lock()
				got = e
				mu.Unlock()
				return outbox.HandleResult{Disposition: outbox.DispositionAck}
			})
	}()

	time.Sleep(20 * time.Millisecond)

	// Business payload without id/eventType — must NOT be treated as envelope.
	direct := []byte(`{"action":"updated","key":"k2","value":"v2"}`)
	require.NoError(t, bus.Publish(context.Background(), "test.direct.topic", direct))

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got.ID != ""
	}, time.Second, 10*time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, direct, got.Payload, "non-envelope payload must be forwarded unchanged")
	assert.Contains(t, got.ID, "evt-", "bus stamps evt-{uuid} id for direct publish")
	assert.Equal(t, "test.direct.topic", got.EventType, "event type defaults to topic for direct publish")
}

func TestPublishSubscribe(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	var received []outbox.Entry
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "retry.topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "reject.topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "perm.requeue"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "race.topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	require.Eventually(t, func() bool {
		bus.mu.RLock()
		defer bus.mu.RUnlock()
		gs := bus.groupSubs["race.topic"][""]
		return gs != nil && len(gs.subs) == 1
	}, time.Second, 10*time.Millisecond)

	var stop atomic.Bool
	var publishStarted atomic.Int32
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
				// CAS ensures exactly one 0→1 transition; the Eventually check
				// below only needs proof that at least one publisher is actively
				// calling Publish before we race Close against them.
				publishStarted.CompareAndSwap(0, 1)
				_ = bus.Publish(context.Background(), "race.topic", []byte("payload"))
			}
		}(i)
	}

	require.Eventually(t, func() bool {
		return publishStarted.Load() == 1
	}, time.Second, 10*time.Millisecond)
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

	err := bus.Subscribe(context.Background(), outbox.Subscription{Topic: "topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "multi.topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			count1.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "multi.topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "partial.fail"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "cleanup.topic"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Wait for subscriber to register.
	time.Sleep(20 * time.Millisecond)

	bus.mu.RLock()
	gs := bus.groupSubs["cleanup.topic"][""]
	subsBefore := 0
	if gs != nil {
		subsBefore = len(gs.subs)
	}
	bus.mu.RUnlock()
	assert.Equal(t, 1, subsBefore, "subscriber should be registered")

	// Cancel the subscriber.
	cancel()
	<-done

	// After exit, the subscription should be removed.
	bus.mu.RLock()
	gs2 := bus.groupSubs["cleanup.topic"][""]
	subsAfter := 0
	if gs2 != nil {
		subsAfter = len(gs2.subs)
	}
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

func (r *mockReceipt) Extend(_ context.Context, _ time.Duration) error {
	return nil
}

func TestSubscribe_ReceiptCommittedOnAck(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	receipt := &mockReceipt{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "receipt.ack"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "receipt.reject"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "receipt.requeue"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "receipt.exhaust"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "zero.disp"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "unknown.disp"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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
		done <- bus.Subscribe(ctx, outbox.Subscription{Topic: "cancel.disp"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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

// ---------------------------------------------------------------------------
// ConsumerGroup tests (ER-ARCH-02)
// ---------------------------------------------------------------------------

// TestConsumerGroup_SameGroup_CompetingConsumption verifies that two subscribers
// in the SAME consumer group compete for messages (round-robin): each message
// goes to exactly one subscriber, not both.
func TestConsumerGroup_SameGroup_CompetingConsumption(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		sub1Count atomic.Int32
		sub2Count atomic.Int32
		wg        sync.WaitGroup
	)

	// Two subscribers in the SAME group "audit-core".
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "session.created", ConsumerGroup: "audit-core"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub1Count.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "session.created", ConsumerGroup: "audit-core"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub2Count.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	time.Sleep(20 * time.Millisecond) // let subscriptions register

	// Publish 10 messages.
	n := 10
	for range n {
		require.NoError(t, bus.Publish(ctx, "session.created", []byte(`{"event":"test"}`)))
	}

	// Wait for all messages to be handled.
	require.Eventually(t, func() bool {
		return int(sub1Count.Load()+sub2Count.Load()) >= n
	}, 2*time.Second, 10*time.Millisecond, "all messages should be consumed")

	cancel()
	wg.Wait()

	total := int(sub1Count.Load() + sub2Count.Load())
	assert.Equal(t, n, total, "total consumed should equal published")

	// Both should have received some (round-robin distributes evenly).
	assert.Greater(t, sub1Count.Load(), int32(0), "sub1 should receive at least 1 message")
	assert.Greater(t, sub2Count.Load(), int32(0), "sub2 should receive at least 1 message")
}

// TestConsumerGroup_DifferentGroups_Fanout verifies that two subscribers in
// DIFFERENT consumer groups each receive a full copy of every message (fanout).
func TestConsumerGroup_DifferentGroups_Fanout(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		auditCount  atomic.Int32
		configCount atomic.Int32
		wg          sync.WaitGroup
	)

	// Two subscribers in DIFFERENT groups.
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "session.created", ConsumerGroup: "audit-core"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			auditCount.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "session.created", ConsumerGroup: "config-core"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			configCount.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	n := 5
	for range n {
		require.NoError(t, bus.Publish(ctx, "session.created", []byte(`{"event":"test"}`)))
	}

	require.Eventually(t, func() bool {
		return int(auditCount.Load()) >= n && int(configCount.Load()) >= n
	}, 2*time.Second, 10*time.Millisecond, "both groups should receive all messages")

	cancel()
	wg.Wait()

	assert.Equal(t, int32(n), auditCount.Load(), "audit-core should get all messages")
	assert.Equal(t, int32(n), configCount.Load(), "config-core should get all messages")
}

// TestConsumerGroup_EmptyGroup_BackwardCompatible verifies that subscribers
// with an empty consumerGroup ("") get broadcast behavior — each subscriber
// receives every message. This preserves backward compatibility.
func TestConsumerGroup_EmptyGroup_BackwardCompatible(t *testing.T) {
	bus := New(WithBufferSize(16))
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		sub1Count atomic.Int32
		sub2Count atomic.Int32
		wg        sync.WaitGroup
	)

	// Two subscribers with EMPTY consumer group — should behave like fanout.
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "events.v1"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub1Count.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "events.v1"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub2Count.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	n := 5
	for range n {
		require.NoError(t, bus.Publish(ctx, "events.v1", []byte(`{"event":"test"}`)))
	}

	require.Eventually(t, func() bool {
		return int(sub1Count.Load()) >= n && int(sub2Count.Load()) >= n
	}, 2*time.Second, 10*time.Millisecond, "both empty-group subs should get all messages")

	cancel()
	wg.Wait()

	assert.Equal(t, int32(n), sub1Count.Load(), "sub1 should receive all messages (broadcast)")
	assert.Equal(t, int32(n), sub2Count.Load(), "sub2 should receive all messages (broadcast)")
}

// TestConsumerGroup_ConcurrentPublish_NoRace verifies that concurrent Publish
// calls on the same topic and consumer group do not race on rrIdx.
// This test exists to guard the P1 fix (atomic.Uint64) and MUST be run with
// -race to be effective.
func TestConsumerGroup_ConcurrentPublish_NoRace(t *testing.T) {
	bus := New(WithBufferSize(256))
	defer func() { _ = bus.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		totalReceived atomic.Int64
		wg            sync.WaitGroup
	)

	// 4 subscribers in the same group — competing.
	const numSubs = 4
	wg.Add(numSubs)
	for range numSubs {
		go func() {
			defer wg.Done()
			_ = bus.Subscribe(ctx, outbox.Subscription{Topic: "race.topic", ConsumerGroup: "race-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				totalReceived.Add(1)
				return outbox.HandleResult{Disposition: outbox.DispositionAck}
			})
		}()
	}

	time.Sleep(20 * time.Millisecond) // let subscriptions register

	// Concurrent publishers hammering the same topic+group.
	const numPublishers = 8
	const msgsPerPublisher = 50
	var pubWg sync.WaitGroup
	pubWg.Add(numPublishers)
	for range numPublishers {
		go func() {
			defer pubWg.Done()
			for range msgsPerPublisher {
				_ = bus.Publish(ctx, "race.topic", []byte(`{"race":"test"}`))
			}
		}()
	}
	pubWg.Wait()

	totalExpected := numPublishers * msgsPerPublisher
	require.Eventually(t, func() bool {
		return totalReceived.Load() >= int64(totalExpected)
	}, 3*time.Second, 10*time.Millisecond,
		"all messages should be consumed: got %d, want %d", totalReceived.Load(), totalExpected)

	cancel()
	wg.Wait()

	assert.Equal(t, int64(totalExpected), totalReceived.Load(),
		"total consumed should equal total published across all concurrent publishers")
}

// Verify interface compliance at compile time.
var (
	_ outbox.Publisher  = (*InMemoryEventBus)(nil)
	_ outbox.Subscriber = (*InMemoryEventBus)(nil)
)
