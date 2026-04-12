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
)

// defaultTimeout is the base timeout for subscribe/collect operations.
const defaultTimeout = 10 * time.Second

// subscribeInitDelay is the time to wait after launching a Subscribe goroutine
// for the subscription to register internally. This is a fixed constant;
// adapter implementations with slower initialization should use a wrapper
// constructor that includes their own warmup delay.
const subscribeInitDelay = 50 * time.Millisecond

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
	done := make(chan struct{})

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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, payload))

	select {
	case <-done:
		assertBytesEqual(t, payload, received.Payload)
		assertTrue(t, received.ID != "", "entry ID must not be empty")
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
	c := startCollecting(t, ctx, sub, topic, n)

	entries := PublishN(t, ctx, pub, topic, n)
	collected := c.waitAndGet(defaultTimeout)

	assertLen(t, len(collected), n, fmt.Sprintf("expected %d messages", n))

	publishedPayloads := make(map[string]bool, len(entries))
	for _, e := range entries {
		publishedPayloads[string(e.Payload)] = true
	}
	for _, entry := range collected {
		assertTrue(t, publishedPayloads[string(entry.Payload)],
			fmt.Sprintf("unexpected payload: %s", string(entry.Payload)))
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

	c := startCollecting(t, ctx, sub, topic, n)

	for i := range n {
		assertNoError(t, pub.Publish(ctx, topic, testPayload(i)))
	}

	collected := c.waitAndGet(defaultTimeout)
	assertLen(t, len(collected), n)

	for i, entry := range collected {
		assertBytesEqual(t, testPayload(i), entry.Payload,
			fmt.Sprintf("message %d: expected seq %d payload", i, i))
	}
}

func testTopicIsolation(t *testing.T, _ Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topicA := TestTopic(t) + "-A"
	topicB := TestTopic(t) + "-B"

	var (
		receivedA  []outbox.Entry
		mu         sync.Mutex
		doneA      = make(chan struct{})
		closeOnceA sync.Once
	)

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
				closeOnceA.Do(func() { close(doneA) })
			}
			mu.Unlock()
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(subscribeInitDelay)

	// Publish to both topics.
	assertNoError(t, pub.Publish(ctx, topicB, []byte(`{"topic":"B"}`)))
	assertNoError(t, pub.Publish(ctx, topicA, []byte(`{"topic":"A"}`)))

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
	assertLen(t, len(receivedA), 1, "subscriber on topic A should only receive topic A messages")
	assertBytesEqual(t, []byte(`{"topic":"A"}`), receivedA[0].Payload)
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

	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"fan-out"}`)))

	// Wait for both subscribers to receive.
	assertEventually(t, func() bool {
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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"ack"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	// Brief pause — Ack should NOT cause redelivery.
	time.Sleep(200 * time.Millisecond)
	assertEqual(t, int32(1), callCount.Load(), "Ack should not cause redelivery")

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

	var (
		callCount atomic.Int32
		done      = make(chan struct{})
		closeOnce sync.Once
	)

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
			closeOnce.Do(func() { close(done) })
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"requeue"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out waiting for redelivery after requeue")
	}

	assertTrue(t, callCount.Load() >= 2,
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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"reject"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	// Brief pause — Reject should NOT cause redelivery.
	time.Sleep(200 * time.Millisecond)
	assertEqual(t, int32(1), callCount.Load(),
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

	var (
		callCount atomic.Int32
		done      = make(chan struct{})
		closeOnce sync.Once
	)

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
			closeOnce.Do(func() { close(done) })
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"zero-disposition"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out — zero-value Disposition should degrade to requeue")
	}

	assertTrue(t, callCount.Load() >= 2,
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

	var (
		callCount atomic.Int32
		done      = make(chan struct{})
		closeOnce sync.Once
	)

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
			closeOnce.Do(func() { close(done) })
			return res
		})
	}()
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"permanent-error"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	time.Sleep(200 * time.Millisecond)
	assertEqual(t, int32(1), callCount.Load(),
		"PermanentError via WrapLegacyHandler should cause reject, not retry")

	cancel()
	<-subDone
}

func testWrapLegacyHandlerSuccess(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error { return nil }
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: "test-1"})
	assertEqual(t, outbox.DispositionAck, res.Disposition)
	assertTrue(t, res.Err == nil, "expected nil error")
}

func testWrapLegacyHandlerTransientError(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error {
		return fmt.Errorf("transient db error")
	}
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: "test-1"})
	assertEqual(t, outbox.DispositionRequeue, res.Disposition)
	assertTrue(t, res.Err != nil, "expected non-nil error")
}

func testWrapLegacyHandlerPermanentError(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error {
		return outbox.NewPermanentError(fmt.Errorf("bad payload"))
	}
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: "test-1"})
	assertEqual(t, outbox.DispositionReject, res.Disposition)
	assertTrue(t, res.Err != nil, "expected non-nil error")

	var permErr *outbox.PermanentError
	assertTrue(t, errors.As(res.Err, &permErr), "expected PermanentError via errors.As")
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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"receipt-ack"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assertEventually(t, func() bool { return receipt.Committed() },
		5*time.Second, 10*time.Millisecond, "Receipt.Commit should be called on Ack")
	assertFalse(t, receipt.Released(), "Receipt.Release should NOT be called on Ack")

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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"receipt-reject"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assertEventually(t, func() bool { return receipt.Released() },
		5*time.Second, 10*time.Millisecond, "Receipt.Release should be called on Reject")
	assertFalse(t, receipt.Committed(), "Receipt.Commit should NOT be called on Reject")

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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"receipt-requeue"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assertEventually(t, func() bool { return receipt.Released() },
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

	// The standard Publisher.Publish(ctx, topic, payload) interface does not
	// carry metadata. Adapter-specific conformance tests must verify metadata
	// round-trip through their own publishing API (e.g., wire-format Entry
	// with populated Metadata field).
	//
	// This test is intentionally a placeholder: setting SupportsMetadata=true
	// signals that the adapter SHOULD provide its own metadata verification
	// test alongside this suite. A future Publisher interface evolution
	// (PublishEntry) could enable a generic metadata test here.
	t.Skip("metadata round-trip requires adapter-specific publishing API — " +
		"adapter tests should verify metadata separately")
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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, sub.Close())

	select {
	case <-subscribeReturned:
		// Good — subscriber terminated after Close.
	case <-time.After(defaultTimeout):
		t.Fatal("Subscribe did not return after Close()")
	}
}

func testCloseIsIdempotent(t *testing.T, constructor PubSubConstructor) {
	_, sub := constructor(t)

	assertNoError(t, sub.Close())

	// Second close should not panic.
	assertNotPanics(t, func() {
		assertNoError(t, sub.Close())
	})
}

func testPublishAfterClose(t *testing.T, constructor PubSubConstructor) {
	pub, sub := constructor(t)

	assertNoError(t, sub.Close())

	// Publishing after close should not panic.
	assertNotPanics(t, func() {
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

	c := startCollecting(t, ctx, sub, topic, n)

	// Publish concurrently — collect errors safely (t.Fatal from goroutine is illegal).
	var (
		pubErrs []error
		pubMu   sync.Mutex
		wg      sync.WaitGroup
	)
	for i := range n {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			if err := pub.Publish(ctx, topic, testPayload(seq)); err != nil {
				pubMu.Lock()
				pubErrs = append(pubErrs, err)
				pubMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	for _, err := range pubErrs {
		t.Errorf("concurrent publish error: %v", err)
	}
	if len(pubErrs) > 0 {
		t.FailNow()
	}

	collected := c.waitAndGet(defaultTimeout)
	assertLen(t, len(collected), n, "all concurrently published messages should arrive")
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
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, []byte(`{"test":"middleware"}`)))

	select {
	case <-done:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out")
	}

	assertTrue(t, middlewareCalled.Load(), "middleware should have been called")

	cancel()
	<-subDone
}
