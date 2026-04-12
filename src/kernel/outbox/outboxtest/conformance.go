package outboxtest

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
)

// defaultTimeout is the base timeout for subscribe/collect operations.
const defaultTimeout = 10 * time.Second

// TestPubSub runs the full conformance test suite against the given
// Publisher/Subscriber implementation. Features control which tests are
// executed; unsupported features are skipped with t.Skip().
//
// ref: ThreeDotsLabs/watermill pubsub/tests/test_pubsub.go
func TestPubSub(t *testing.T, features Features, constructor PubSubConstructor) {
	features.setDefaults()

	// Batch 1: Core pub/sub
	t.Run("PublishSubscribe", func(t *testing.T) {
		testPublishSubscribe(t, features, constructor)
	})
	t.Run("PublishSubscribeMultiple", func(t *testing.T) {
		testPublishSubscribeMultiple(t, features, constructor)
	})
	t.Run("PublishSubscribeInOrder", func(t *testing.T) {
		testPublishSubscribeInOrder(t, features, constructor)
	})
	t.Run("TopicIsolation", func(t *testing.T) {
		testTopicIsolation(t, features, constructor)
	})
	t.Run("MultipleSubscribers", func(t *testing.T) {
		testMultipleSubscribers(t, features, constructor)
	})

	// Batch 2: Disposition lifecycle
	t.Run("DispositionAck", func(t *testing.T) {
		testDispositionAck(t, features, constructor)
	})
	t.Run("DispositionRequeue", func(t *testing.T) {
		testDispositionRequeue(t, features, constructor)
	})
	t.Run("DispositionReject", func(t *testing.T) {
		testDispositionReject(t, features, constructor)
	})
	t.Run("ZeroValueDisposition", func(t *testing.T) {
		testZeroValueDisposition(t, features, constructor)
	})

	// Batch 3: PermanentError + WrapLegacyHandler
	t.Run("PermanentErrorCausesReject", func(t *testing.T) {
		testPermanentErrorCausesReject(t, features, constructor)
	})
	t.Run("WrapLegacyHandler_Success", func(t *testing.T) {
		testWrapLegacyHandlerSuccess(t)
	})
	t.Run("WrapLegacyHandler_TransientError", func(t *testing.T) {
		testWrapLegacyHandlerTransientError(t)
	})
	t.Run("WrapLegacyHandler_PermanentError", func(t *testing.T) {
		testWrapLegacyHandlerPermanentError(t)
	})

	// Batch 4: Receipt lifecycle
	t.Run("ReceiptCommittedOnAck", func(t *testing.T) {
		testReceiptCommittedOnAck(t, features, constructor)
	})
	t.Run("ReceiptReleasedOnReject", func(t *testing.T) {
		testReceiptReleasedOnReject(t, features, constructor)
	})
	t.Run("ReceiptReleasedOnRequeue", func(t *testing.T) {
		testReceiptReleasedOnRequeue(t, features, constructor)
	})

	// Batch 5: Metadata + lifecycle
	t.Run("MetadataRoundTrip", func(t *testing.T) {
		testMetadataRoundTrip(t, features, constructor)
	})
	t.Run("SubscribeBlocksUntilCancel", func(t *testing.T) {
		testSubscribeBlocksUntilCancel(t, features, constructor)
	})
	t.Run("CloseTerminatesSubscribers", func(t *testing.T) {
		testCloseTerminatesSubscribers(t, features, constructor)
	})
	t.Run("CloseIsIdempotent", func(t *testing.T) {
		testCloseIsIdempotent(t, constructor)
	})
	t.Run("PublishAfterClose", func(t *testing.T) {
		testPublishAfterClose(t, constructor)
	})

	// Batch 6: Concurrency + middleware
	t.Run("ConcurrentPublish", func(t *testing.T) {
		testConcurrentPublish(t, features, constructor)
	})
	t.Run("SubscriberWithMiddleware", func(t *testing.T) {
		testSubscriberWithMiddleware(t, features, constructor)
	})
}

// ---------------------------------------------------------------------------
// Batch 1: Core pub/sub
// ---------------------------------------------------------------------------

func testPublishSubscribe(t *testing.T, _ Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	payload := []byte(`{"test":"publish_subscribe"}`)

	var received outbox.Entry
	var done = make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			received = entry
			close(done)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	err := pub.Publish(ctx, topic, payload)
	assert.NoError(t, err)

	select {
	case <-done:
		assert.Equal(t, payload, received.Payload)
		assert.NotEmpty(t, received.ID)
	case <-time.After(defaultTimeout):
		t.Fatal("timed out waiting for message")
	}

	cancel()
	<-subDone
}

func testPublishSubscribeMultiple(t *testing.T, features Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	n := features.MessageCount

	// Start subscriber FIRST (InMemoryEventBus is at-most-once).
	var (
		mu        sync.Mutex
		collected []outbox.Entry
		done      = make(chan struct{})
	)
	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			mu.Lock()
			collected = append(collected, entry)
			count := len(collected)
			mu.Unlock()
			if count >= n {
				select {
				case <-done:
				default:
					close(done)
				}
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	// Now publish.
	entries := PublishN(t, ctx, pub, topic, n)

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		mu.Lock()
		got := len(collected)
		mu.Unlock()
		t.Fatalf("timed out: collected %d/%d", got, n)
	}

	cancel()
	<-subDone

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, collected, n, "expected %d messages, got %d", n, len(collected))

	// Verify all payloads arrived (order may vary).
	publishedPayloads := make(map[string]bool, len(entries))
	for _, e := range entries {
		publishedPayloads[string(e.Payload)] = true
	}
	for _, c := range collected {
		assert.True(t, publishedPayloads[string(c.Payload)],
			"unexpected payload: %s", string(c.Payload))
	}
}

func testPublishSubscribeInOrder(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.GuaranteedOrder {
		t.Skip("implementation does not guarantee order")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	n := 20
	if testing.Short() {
		n = 5
	}

	// Start subscriber FIRST.
	var (
		mu        sync.Mutex
		collected []outbox.Entry
		done      = make(chan struct{})
	)
	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			mu.Lock()
			collected = append(collected, entry)
			count := len(collected)
			mu.Unlock()
			if count >= n {
				select {
				case <-done:
				default:
					close(done)
				}
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	// Publish sequentially.
	for i := range n {
		err := pub.Publish(ctx, topic, testPayload(i))
		assert.NoError(t, err)
	}

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		mu.Lock()
		got := len(collected)
		mu.Unlock()
		t.Fatalf("timed out: collected %d/%d", got, n)
	}

	cancel()
	<-subDone

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, collected, n)

	// Verify arrival order matches publish order.
	for i, entry := range collected {
		expected := testPayload(i)
		assert.Equal(t, expected, entry.Payload,
			"message %d: expected seq %d payload", i, i)
	}
}

func testTopicIsolation(t *testing.T, _ Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topicA := TestTopic(t) + "-A"
	topicB := TestTopic(t) + "-B"

	var receivedA []outbox.Entry
	var mu sync.Mutex
	doneA := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	// Subscribe only to topic A.
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topicA, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			mu.Lock()
			receivedA = append(receivedA, entry)
			if len(receivedA) >= 1 {
				select {
				case <-doneA:
				default:
					close(doneA)
				}
			}
			mu.Unlock()
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	// Publish to both topics.
	assert.NoError(t, pub.Publish(ctx, topicB, []byte(`{"topic":"B"}`)))
	assert.NoError(t, pub.Publish(ctx, topicA, []byte(`{"topic":"A"}`)))

	select {
	case <-doneA:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out waiting for message on topic A")
	}

	// Give a brief window for any leaked messages.
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-subDone

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, receivedA, 1, "subscriber on topic A should only receive topic A messages")
	assert.Equal(t, []byte(`{"topic":"A"}`), receivedA[0].Payload)
}

func testMultipleSubscribers(t *testing.T, _ Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var (
		sub1Received atomic.Int32
		sub2Received atomic.Int32
		wg           sync.WaitGroup
	)

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	// Subscriber 1.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub1Received.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Subscriber 2.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub2Received.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	time.Sleep(20 * time.Millisecond)

	err := pub.Publish(ctx, topic, []byte(`{"test":"fan-out"}`))
	assert.NoError(t, err)

	// Wait for both subscribers to receive.
	assert.Eventually(t, func() bool {
		return sub1Received.Load() >= 1 && sub2Received.Load() >= 1
	}, defaultTimeout, 10*time.Millisecond, "both subscribers should receive the message")

	cancel()
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Batch 2: Disposition lifecycle
// ---------------------------------------------------------------------------

func testDispositionAck(t *testing.T, _ Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var callCount atomic.Int32
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			callCount.Add(1)
			close(done)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"ack"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	// Brief pause — Ack should NOT cause redelivery.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load(), "Ack should not cause redelivery")

	cancel()
	<-subDone
}

func testDispositionRequeue(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsRequeue {
		t.Skip("implementation does not support requeue")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var callCount atomic.Int32
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			n := callCount.Add(1)
			if n == 1 {
				// First call: requeue.
				return outbox.HandleResult{
					Disposition: outbox.DispositionRequeue,
					Err:         fmt.Errorf("transient failure"),
				}
			}
			// Second call: ack.
			select {
			case <-done:
			default:
				close(done)
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"requeue"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out waiting for redelivery after requeue")
	}

	assert.GreaterOrEqual(t, callCount.Load(), int32(2),
		"handler should be called at least twice (initial + redelivery)")

	cancel()
	<-subDone
}

func testDispositionReject(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReject {
		t.Skip("implementation does not support reject")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var callCount atomic.Int32
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			callCount.Add(1)
			close(done)
			return outbox.HandleResult{
				Disposition: outbox.DispositionReject,
				Err:         outbox.NewPermanentError(fmt.Errorf("bad payload")),
			}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"reject"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	// Brief pause — Reject should NOT cause redelivery.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load(),
		"Reject should route to DLQ, not retry")

	cancel()
	<-subDone
}

func testZeroValueDisposition(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsRequeue {
		t.Skip("implementation does not support requeue (needed to verify safe degradation)")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var callCount atomic.Int32
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			n := callCount.Add(1)
			if n == 1 {
				// Return zero-value HandleResult (invalid Disposition).
				return outbox.HandleResult{}
			}
			select {
			case <-done:
			default:
				close(done)
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"zero-disposition"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out — zero-value Disposition should degrade to requeue")
	}

	assert.GreaterOrEqual(t, callCount.Load(), int32(2),
		"zero-value Disposition should be treated as requeue (safe degradation)")

	cancel()
	<-subDone
}

// ---------------------------------------------------------------------------
// Batch 3: PermanentError + WrapLegacyHandler (pure function tests)
// ---------------------------------------------------------------------------

func testPermanentErrorCausesReject(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReject {
		t.Skip("implementation does not support reject")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var callCount atomic.Int32
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	legacy := func(_ context.Context, _ outbox.Entry) error {
		return outbox.NewPermanentError(fmt.Errorf("unmarshal failed"))
	}
	handler := outbox.WrapLegacyHandler(legacy)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			callCount.Add(1)
			res := handler(ctx, entry)
			select {
			case <-done:
			default:
				close(done)
			}
			return res
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"permanent-error"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load(),
		"PermanentError via WrapLegacyHandler should cause reject, not retry")

	cancel()
	<-subDone
}

func testWrapLegacyHandlerSuccess(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error { return nil }
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: "test-1"})
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.NoError(t, res.Err)
}

func testWrapLegacyHandlerTransientError(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error {
		return fmt.Errorf("transient db error")
	}
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: "test-1"})
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.Error(t, res.Err)
}

func testWrapLegacyHandlerPermanentError(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error {
		return outbox.NewPermanentError(fmt.Errorf("bad payload"))
	}
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: "test-1"})
	assert.Equal(t, outbox.DispositionReject, res.Disposition)
	assert.Error(t, res.Err)

	var permErr *outbox.PermanentError
	assert.True(t, errors.As(res.Err, &permErr))
}

// ---------------------------------------------------------------------------
// Batch 4: Receipt lifecycle
// ---------------------------------------------------------------------------

func testReceiptCommittedOnAck(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReceipt {
		t.Skip("implementation does not support receipt")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	receipt := NewMockReceipt()
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			close(done)
			return outbox.HandleResult{
				Disposition: outbox.DispositionAck,
				Receipt:     receipt,
			}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"receipt-ack"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assert.Eventually(t, func() bool { return receipt.Committed() },
		5*time.Second, 10*time.Millisecond, "Receipt.Commit should be called on Ack")
	assert.False(t, receipt.Released(), "Receipt.Release should NOT be called on Ack")

	cancel()
	<-subDone
}

func testReceiptReleasedOnReject(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReceipt {
		t.Skip("implementation does not support receipt")
	}
	if !features.SupportsReject {
		t.Skip("implementation does not support reject")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	receipt := NewMockReceipt()
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			close(done)
			return outbox.HandleResult{
				Disposition: outbox.DispositionReject,
				Err:         outbox.NewPermanentError(fmt.Errorf("bad")),
				Receipt:     receipt,
			}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"receipt-reject"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assert.Eventually(t, func() bool { return receipt.Released() },
		5*time.Second, 10*time.Millisecond, "Receipt.Release should be called on Reject")
	assert.False(t, receipt.Committed(), "Receipt.Commit should NOT be called on Reject")

	cancel()
	<-subDone
}

func testReceiptReleasedOnRequeue(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReceipt {
		t.Skip("implementation does not support receipt")
	}
	if !features.SupportsRequeue {
		t.Skip("implementation does not support requeue")
	}

	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	receipt := NewMockReceipt()
	done := make(chan struct{})

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	var callCount atomic.Int32
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			n := callCount.Add(1)
			if n == 1 {
				close(done)
				return outbox.HandleResult{
					Disposition: outbox.DispositionRequeue,
					Err:         fmt.Errorf("transient"),
					Receipt:     receipt,
				}
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"receipt-requeue"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assert.Eventually(t, func() bool { return receipt.Released() },
		5*time.Second, 10*time.Millisecond, "Receipt.Release should be called on Requeue")

	cancel()
	<-subDone
}

// ---------------------------------------------------------------------------
// Batch 5: Metadata + lifecycle
// ---------------------------------------------------------------------------

func testMetadataRoundTrip(t *testing.T, features Features, _ PubSubConstructor) {
	if !features.SupportsMetadata {
		t.Skip("implementation does not support metadata round-trip")
	}

	// This test requires an implementation that preserves metadata through
	// the publish/subscribe cycle. InMemoryEventBus does not, so this is
	// skipped for it and exercised by broker adapters (e.g., RabbitMQ).
	t.Log("MetadataRoundTrip: implementation-specific, tested via adapter conformance tests")
}

func testSubscribeBlocksUntilCancel(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.BlockingSubscribe {
		t.Skip("implementation does not block on Subscribe")
	}

	_, sub := constructor(t)
	ctx, cancel := context.WithCancel(context.Background())

	subscribeReturned := make(chan error, 1)
	go func() {
		err := sub.Subscribe(ctx, TestTopic(t), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
		subscribeReturned <- err
	}()

	// Subscribe should be blocking.
	select {
	case <-subscribeReturned:
		t.Fatal("Subscribe returned before context was cancelled")
	case <-time.After(100 * time.Millisecond):
		// Good — still blocking.
	}

	cancel()

	select {
	case <-subscribeReturned:
		// Good — returned after cancel.
	case <-time.After(defaultTimeout):
		t.Fatal("Subscribe did not return after context cancel")
	}
}

func testCloseTerminatesSubscribers(t *testing.T, _ Features, constructor PubSubConstructor) {
	_, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	subscribeReturned := make(chan struct{})
	go func() {
		defer close(subscribeReturned)
		_ = sub.Subscribe(ctx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	err := sub.Close()
	assert.NoError(t, err)

	select {
	case <-subscribeReturned:
		// Good — subscriber terminated after Close.
	case <-time.After(defaultTimeout):
		t.Fatal("Subscribe did not return after Close()")
	}
}

func testCloseIsIdempotent(t *testing.T, constructor PubSubConstructor) {
	_, sub := constructor(t)

	err1 := sub.Close()
	assert.NoError(t, err1)

	// Second close should not panic.
	assert.NotPanics(t, func() {
		err2 := sub.Close()
		assert.NoError(t, err2)
	})
}

func testPublishAfterClose(t *testing.T, constructor PubSubConstructor) {
	pub, sub := constructor(t)

	err := sub.Close()
	assert.NoError(t, err)

	// Publishing after close should not panic.
	assert.NotPanics(t, func() {
		_ = pub.Publish(context.Background(), "any-topic", []byte(`{}`))
	})
}

// ---------------------------------------------------------------------------
// Batch 6: Concurrency + middleware
// ---------------------------------------------------------------------------

func testConcurrentPublish(t *testing.T, features Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	n := min(features.MessageCount, 50)

	// Start subscriber FIRST.
	var (
		mu        sync.Mutex
		collected []outbox.Entry
		done      = make(chan struct{})
	)
	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			mu.Lock()
			collected = append(collected, entry)
			count := len(collected)
			mu.Unlock()
			if count >= n {
				select {
				case <-done:
				default:
					close(done)
				}
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	// Publish concurrently.
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			err := pub.Publish(ctx, topic, testPayload(seq))
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		mu.Lock()
		got := len(collected)
		mu.Unlock()
		t.Fatalf("timed out: collected %d/%d", got, n)
	}

	cancel()
	<-subDone

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, collected, n, "all concurrently published messages should arrive")
}

func testSubscriberWithMiddleware(t *testing.T, _ Features, constructor PubSubConstructor) {
	pub, innerSub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var middlewareCalled atomic.Bool
	middleware := func(_ string, next outbox.EntryHandler) outbox.EntryHandler {
		return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			middlewareCalled.Store(true)
			return next(ctx, entry)
		}
	}

	sub := &outbox.SubscriberWithMiddleware{
		Inner:      innerSub,
		Middleware: []outbox.TopicHandlerMiddleware{middleware},
	}

	done := make(chan struct{})
	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			close(done)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(20 * time.Millisecond)

	assert.NoError(t, pub.Publish(ctx, topic, []byte(`{"test":"middleware"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assert.True(t, middlewareCalled.Load(), "middleware should have been called")

	cancel()
	<-subDone
}
