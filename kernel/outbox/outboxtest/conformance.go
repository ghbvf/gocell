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

// Skip message constants to avoid SonarCloud string duplication warnings.
const (
	skipNoReject  = "implementation does not support reject"
	skipNoReceipt = "implementation does not support receipt"
)

// testEntryID is the canonical entry ID used in pure-function tests.
const testEntryID = "test-1"

// subscribeInitDelay is the time to wait after launching a Subscribe goroutine
// for the subscription to register internally. This is a fixed constant;
// adapter implementations with slower initialization should use a wrapper
// constructor that includes their own warmup delay.
const subscribeInitDelay = 50 * time.Millisecond

// negativeAssertionWindow bounds how long "no further delivery" assertions
// wait before returning a pass. 200ms is empirically adequate for the
// in-memory bus and RabbitMQ adapter (observed ack RTT single-digit ms);
// select-based fail-fast returns in ms on actual violations, so the window
// only extends the success path.
//
// CI flake guidance: if this window produces false negatives on a busy
// runner, raise it here (propagates to all negative-assertion call sites)
// rather than tweaking individual tests.
const negativeAssertionWindow = 200 * time.Millisecond

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
		if !features.BroadcastSubscribe {
			t.Skip("implementation uses competing consumers, not broadcast fan-out")
		}
		testMultipleSubscribers(t, features, constructor)
	})
	t.Run("CompetingConsumers", func(t *testing.T) {
		if features.BroadcastSubscribe {
			t.Skip("implementation uses broadcast fan-out, not competing consumers")
		}
		testCompetingConsumers(t, features, constructor)
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
	t.Run("ReceiptCommitFailureDoesNotAck", func(t *testing.T) {
		testReceiptCommitFailureDoesNotAck(t, features, constructor)
	})

	// Batch 5: Lifecycle
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
	h := newHarness(t, constructor)
	payload := []byte(`{"test":"publish_subscribe"}`)

	var received outbox.Entry
	h.subscribe(func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
		received = entry
		h.signalDone()
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	h.publishAndWait(payload)
	assertBytesEqual(t, payload, received.Payload)
	assertTrue(t, received.ID != "", "entry ID must not be empty")
	h.teardown()
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
		assertNoError(t, pub.Publish(ctx, topic, wrapV1Envelope(t, topic, testPayload(i))))
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
		// deliveryA tracks every delivery to topic A for fail-fast
		// negative-assertion: any leak from topic B shows up as an extra event.
		deliveryA = make(chan struct{}, deliveryEventsBuffer)
	)

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	// Subscribe only to topic A.
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, outbox.Subscription{Topic: topicA}, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			select {
			case deliveryA <- struct{}{}:
			default:
			}
			mu.Lock()
			receivedA = append(receivedA, entry)
			if len(receivedA) >= 1 {
				closeOnceA.Do(func() { close(doneA) })
			}
			mu.Unlock()
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	waitForSubscription(t, ctx, sub, topicA, "")

	// Publish to both topics.
	assertNoError(t, pub.Publish(ctx, topicB, wrapV1Envelope(t, topicB, []byte(`{"topic":"B"}`))))
	assertNoError(t, pub.Publish(ctx, topicA, wrapV1Envelope(t, topicA, []byte(`{"topic":"A"}`))))

	select {
	case <-doneA:
	case <-time.After(defaultTimeout):
		t.Fatal("timed out waiting for message on topic A")
	}

	// Drain the expected topicA delivery. This receive is unbounded-safe: the
	// handler sends to deliveryA *before* closing doneA, and <-doneA above was
	// already bounded by defaultTimeout, so deliveryA has a buffered event by
	// this point. Do NOT reorder the handler logic without re-evaluating.
	<-deliveryA
	// Fail-fast if another arrives (i.e. a topicB message leaked to topicA).
	select {
	case <-deliveryA:
		t.Fatal("topic A received an unexpected message (topic B leak)")
	case <-time.After(negativeAssertionWindow):
	}

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
		sub1Ready    = make(chan struct{})
		sub2Ready    = make(chan struct{})
	)

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	// Per-sub readiness signals: Subscribe is blocking, and the bus-level
	// Ready() channel only synchronizes the FIRST Subscribe call for a given
	// (consumerGroup, topic) pair. With two broadcast subscribers we need each
	// goroutine to confirm its own registration before the test publishes,
	// otherwise the second sub may miss the event.
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(sub1Ready)
		_ = sub.Subscribe(subCtx, outbox.Subscription{Topic: topic}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub1Received.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		close(sub2Ready)
		_ = sub.Subscribe(subCtx, outbox.Subscription{Topic: topic}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			sub2Received.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	<-sub1Ready
	<-sub2Ready
	waitForSubscription(t, ctx, sub, topic, "")
	// In-memory bus signals Ready after the FIRST Subscribe registration; the
	// second broadcast sub may still be entering its Subscribe call. A brief
	// sleep covers the tail registration window without coupling the test to
	// bus internals. Persistent brokers (RabbitMQ) skip this path entirely
	// (BroadcastSubscribe=false → testCompetingConsumers).
	time.Sleep(subscribeInitDelay)

	assertNoError(t, pub.Publish(ctx, topic, wrapV1Envelope(t, topic, []byte(`{"test":"fan-out"}`))))

	// Wait for both subscribers to receive.
	assertEventually(t, func() bool {
		return sub1Received.Load() >= 1 && sub2Received.Load() >= 1
	}, defaultTimeout, 10*time.Millisecond, "both subscribers should receive the message")

	cancel()
	wg.Wait()
}

// testCompetingConsumers verifies that when BroadcastSubscribe=false (e.g.,
// RabbitMQ with a shared queue), a single message is delivered to exactly one
// of multiple subscribers — not duplicated to all.
// Features is unused here; the signature matches the test-registration interface.
func testCompetingConsumers(t *testing.T, _ Features, constructor PubSubConstructor) {
	pub, sub := constructor(t)
	ctx := context.Background()
	topic := TestTopic(t)

	var (
		totalReceived atomic.Int32
		wg            sync.WaitGroup
		// delivery channels emit one non-blocking event per delivery; drained
		// after the expected single delivery, any further event is a duplicate.
		delivery = make(chan struct{}, deliveryEventsBuffer)
	)

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	// Start two competing subscribers on the same topic.
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sub.Subscribe(subCtx, outbox.Subscription{Topic: topic}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				select {
				case delivery <- struct{}{}:
				default:
				}
				totalReceived.Add(1)
				return outbox.HandleResult{Disposition: outbox.DispositionAck}
			})
		}()
	}

	// One waitForSubscription is sufficient for shared-queue brokers: both
	// competing subscribers consume from the same queue, and topology is
	// created once. The sleep fallback covers in-memory subscribers where
	// both goroutines need time to register.
	waitForSubscription(t, ctx, sub, topic, "")

	// Publish one message.
	assertNoError(t, pub.Publish(ctx, topic, wrapV1Envelope(t, topic, []byte(`{"test":"competing"}`))))

	// Wait for exactly one delivery (bounded — if nothing arrives within
	// defaultTimeout the test fails fast rather than hanging until the global
	// go-test deadline), then fail-fast if any duplicate arrives within the
	// negative-assertion window.
	select {
	case <-delivery:
	case <-time.After(defaultTimeout):
		t.Fatalf("competing consumers: no delivery within %s (subscriber registration or publish failed)", defaultTimeout)
	}
	select {
	case <-delivery:
		t.Fatalf("competing consumers: duplicate delivery detected within %s window", negativeAssertionWindow)
	case <-time.After(negativeAssertionWindow):
	}

	got := totalReceived.Load()
	assertEqual(t, int32(1), got,
		fmt.Sprintf("competing consumers: message should be delivered to exactly 1 subscriber, got %d", got))

	cancel()
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Batch 2: Disposition lifecycle
// ---------------------------------------------------------------------------

func testDispositionAck(t *testing.T, _ Features, constructor PubSubConstructor) {
	h := newHarness(t, constructor)
	var callCount atomic.Int32

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount.Add(1)
		h.signalDone()
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	h.publishAndWait([]byte(`{"test":"ack"}`))
	// fail-fast: 1 prior delivery expected; no redelivery within the window.
	h.assertNoMoreDeliveries(1, negativeAssertionWindow, "Ack should not cause redelivery")
	assertEqual(t, int32(1), callCount.Load(), "Ack should not cause redelivery")
	h.teardown()
}

func testDispositionRequeue(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsRequeue {
		t.Skip("implementation does not support requeue")
	}

	h := newHarness(t, constructor)
	var callCount atomic.Int32

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		n := callCount.Add(1)
		if n == 1 {
			return outbox.HandleResult{
				Disposition: outbox.DispositionRequeue,
				Err:         fmt.Errorf("transient failure"),
			}
		}
		h.signalDone()
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	h.publishAndWait([]byte(`{"test":"requeue"}`))
	assertTrue(t, callCount.Load() >= 2,
		"handler should be called at least twice (initial + redelivery)")
	h.teardown()
}

func testDispositionReject(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReject {
		t.Skip(skipNoReject)
	}

	h := newHarness(t, constructor)
	var callCount atomic.Int32

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount.Add(1)
		h.signalDone()
		return outbox.HandleResult{
			Disposition: outbox.DispositionReject,
			Err:         outbox.NewPermanentError(fmt.Errorf("bad payload")),
		}
	})

	h.publishAndWait([]byte(`{"test":"reject"}`))
	// fail-fast: 1 prior delivery; Reject should route to DLQ, not retry.
	h.assertNoMoreDeliveries(1, negativeAssertionWindow, "Reject should route to DLQ, not retry")
	assertEqual(t, int32(1), callCount.Load(), "Reject should route to DLQ, not retry")
	h.teardown()
}

func testZeroValueDisposition(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsRequeue {
		t.Skip("implementation does not support requeue (needed to verify safe degradation)")
	}

	h := newHarness(t, constructor)
	var callCount atomic.Int32

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		n := callCount.Add(1)
		if n == 1 {
			return outbox.HandleResult{} // zero-value = invalid Disposition
		}
		h.signalDone()
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	h.publishAndWait([]byte(`{"test":"zero-disposition"}`))
	assertTrue(t, callCount.Load() >= 2,
		"zero-value Disposition should be treated as requeue (safe degradation)")
	h.teardown()
}

// ---------------------------------------------------------------------------
// Batch 3: PermanentError + WrapLegacyHandler (pure function tests)
// ---------------------------------------------------------------------------

func testPermanentErrorCausesReject(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReject {
		t.Skip(skipNoReject)
	}

	h := newHarness(t, constructor)
	var callCount atomic.Int32
	legacy := outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error {
		return outbox.NewPermanentError(fmt.Errorf("unmarshal failed"))
	})

	h.subscribe(func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		callCount.Add(1)
		res := legacy(ctx, entry)
		h.signalDone()
		return res
	})

	h.publishAndWait([]byte(`{"test":"permanent-error"}`))
	// fail-fast: 1 prior delivery; PermanentError via WrapLegacyHandler should reject.
	h.assertNoMoreDeliveries(1, negativeAssertionWindow,
		"PermanentError via WrapLegacyHandler should cause reject, not retry")
	assertEqual(t, int32(1), callCount.Load(),
		"PermanentError via WrapLegacyHandler should cause reject, not retry")
	h.teardown()
}

func testWrapLegacyHandlerSuccess(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error { return nil }
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: testEntryID})
	assertEqual(t, outbox.DispositionAck, res.Disposition)
	assertTrue(t, res.Err == nil, "expected nil error")
}

func testWrapLegacyHandlerTransientError(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error {
		return fmt.Errorf("transient db error")
	}
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: testEntryID})
	assertEqual(t, outbox.DispositionRequeue, res.Disposition)
	assertTrue(t, res.Err != nil, "expected non-nil error")
}

func testWrapLegacyHandlerPermanentError(t *testing.T) {
	legacy := func(_ context.Context, _ outbox.Entry) error {
		return outbox.NewPermanentError(fmt.Errorf("bad payload"))
	}
	handler := outbox.WrapLegacyHandler(legacy)

	res := handler(context.Background(), outbox.Entry{ID: testEntryID})
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
		t.Skip(skipNoReceipt)
	}

	h := newHarness(t, constructor)
	receipt := NewMockReceipt()

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		h.signalDone()
		return outbox.HandleResult{Disposition: outbox.DispositionAck, Receipt: receipt}
	})

	h.publishAndWait([]byte(`{"test":"receipt-ack"}`))
	assertEventually(t, func() bool { return receipt.Committed() },
		5*time.Second, 10*time.Millisecond, "Receipt.Commit should be called on Ack")
	assertFalse(t, receipt.Released(), "Receipt.Release should NOT be called on Ack")
	h.teardown()
}

func testReceiptReleasedOnReject(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReceipt {
		t.Skip(skipNoReceipt)
	}
	if !features.SupportsReject {
		t.Skip(skipNoReject)
	}

	h := newHarness(t, constructor)
	receipt := NewMockReceipt()

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		h.signalDone()
		return outbox.HandleResult{
			Disposition: outbox.DispositionReject,
			Err:         outbox.NewPermanentError(fmt.Errorf("bad")),
			Receipt:     receipt,
		}
	})

	h.publishAndWait([]byte(`{"test":"receipt-reject"}`))
	assertEventually(t, func() bool { return receipt.Released() },
		5*time.Second, 10*time.Millisecond, "Receipt.Release should be called on Reject")
	assertFalse(t, receipt.Committed(), "Receipt.Commit should NOT be called on Reject")
	h.teardown()
}

func testReceiptReleasedOnRequeue(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReceipt {
		t.Skip(skipNoReceipt)
	}
	if !features.SupportsRequeue {
		t.Skip("implementation does not support requeue")
	}

	h := newHarness(t, constructor)
	receipt := NewMockReceipt()
	var callCount atomic.Int32

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		n := callCount.Add(1)
		if n == 1 {
			h.signalDone()
			return outbox.HandleResult{
				Disposition: outbox.DispositionRequeue,
				Err:         fmt.Errorf("transient"),
				Receipt:     receipt,
			}
		}
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	h.publishAndWait([]byte(`{"test":"receipt-requeue"}`))
	assertEventually(t, func() bool { return receipt.Released() },
		5*time.Second, 10*time.Millisecond, "Receipt.Release should be called on Requeue")
	h.teardown()
}

// testReceiptCommitFailureDoesNotAck guards the cross-transport invariant
// added in the F2 fix (PR #184): when handler returns DispositionAck but
// Receipt.Commit fails (lease lost / token mismatch / backend error), the
// adapter must NOT treat the message as successfully acknowledged. RabbitMQ
// translates this to Nack(requeue=true); InMemoryEventBus retries via its
// internal retry loop. Either way the handler must be re-invoked, evidenced
// by an additional Receipt.Commit attempt or a redelivery.
//
// Without this guard, regression to the pre-F2 semantics (eventbus silently
// promoting Commit failure to success) would cause stale lease holders to
// "succeed" — see PR #184 review F2 / commit 87475b9.
func testReceiptCommitFailureDoesNotAck(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.SupportsReceipt {
		t.Skip(skipNoReceipt)
	}

	h := newHarness(t, constructor)
	// Receipt whose Commit always returns an error — emulates lease-lost /
	// token-mismatch backend response.
	receipt := NewMockReceiptWithErrors(errors.New("commit fails (test)"), nil)

	var deliveries atomic.Int32
	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		n := deliveries.Add(1)
		if n == 1 {
			h.signalDone() // wake publisher after first delivery
		}
		return outbox.HandleResult{Disposition: outbox.DispositionAck, Receipt: receipt}
	})

	h.publishAndWait([]byte(`{"test":"commit-fail"}`))
	// Adapter must retry / redeliver after Commit failure. We assert at least
	// one extra Commit attempt OR an extra delivery within a generous window.
	// Both rabbitmq (Nack→requeue→re-consume) and eventbus (handleWithRetry
	// loop) satisfy "Commit was tried more than once" within retry budget.
	assertEventually(t, func() bool {
		return receipt.CommitCount() >= 2 || deliveries.Load() >= 2
	}, 5*time.Second, 20*time.Millisecond,
		"adapter must NOT promote Commit failure to success — expected retry/redelivery")
	h.teardown()
}

// ---------------------------------------------------------------------------
// Batch 5: Metadata + lifecycle
// ---------------------------------------------------------------------------

func testSubscribeBlocksUntilCancel(t *testing.T, features Features, constructor PubSubConstructor) {
	if !features.BlockingSubscribe {
		t.Skip("implementation does not block on Subscribe")
	}

	_, sub := constructor(t)
	ctx, cancel := context.WithCancel(context.Background())

	subscribeReturned := make(chan error, 1)
	go func() {
		err := sub.Subscribe(ctx, outbox.Subscription{Topic: TestTopic(t)}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
		_ = sub.Subscribe(ctx, outbox.Subscription{Topic: topic}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	waitForSubscription(t, ctx, sub, topic, "")

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
		_ = pub.Publish(context.Background(), "any-topic", wrapV1Envelope(t, "any-topic", []byte(`{}`)))
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
			if err := pub.Publish(ctx, topic, wrapV1Envelope(t, topic, testPayload(seq))); err != nil {
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
	h := newHarness(t, constructor)

	var middlewareCalled atomic.Bool
	middleware := func(_ outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			middlewareCalled.Store(true)
			return next(ctx, entry)
		}
	}

	// Wrap inner subscriber with middleware.
	h.Sub = &outbox.SubscriberWithMiddleware{
		Inner:      h.Sub,
		Middleware: []outbox.SubscriptionMiddleware{middleware},
	}

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		h.signalDone()
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	h.publishAndWait([]byte(`{"test":"middleware"}`))
	assertTrue(t, middlewareCalled.Load(), "middleware should have been called")
	h.teardown()
}
