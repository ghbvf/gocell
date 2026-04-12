package outboxtest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// TestTopic returns a unique topic name scoped to the given test.
// Prevents cross-test interference when implementations share state.
func TestTopic(t *testing.T) string {
	t.Helper()
	short := uuid.NewString()[:8]
	return fmt.Sprintf("test-%s-%s", t.Name(), short)
}

// NewEntry creates a valid Entry with a unique ID for testing.
func NewEntry(topic string, payload []byte) outbox.Entry {
	return outbox.Entry{
		ID:        "evt-" + uuid.NewString(),
		EventType: topic,
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
}

// NewEntryWithMetadata creates a valid Entry with metadata.
func NewEntryWithMetadata(topic string, payload []byte, metadata map[string]string) outbox.Entry {
	e := NewEntry(topic, payload)
	e.Metadata = metadata
	return e
}

// testPayload creates a JSON payload with a sequence number.
func testPayload(seq int) []byte {
	data, _ := json.Marshal(map[string]int{"seq": seq})
	return data
}

// PublishN publishes n messages to the given topic and returns the entries published.
func PublishN(t *testing.T, ctx context.Context, pub outbox.Publisher, topic string, n int) []outbox.Entry {
	t.Helper()
	entries := make([]outbox.Entry, 0, n)
	for i := range n {
		payload := testPayload(i)
		entry := NewEntry(topic, payload)
		err := pub.Publish(ctx, topic, payload)
		assert.NoError(t, err, "publish message %d", i)
		entries = append(entries, entry)
	}
	return entries
}

// CollectN subscribes and collects exactly n entries, with a timeout.
// It launches Subscribe in a goroutine (blocking interface) and collects
// via mutex+slice. Returns collected entries. Fails the test on timeout.
func CollectN(
	t *testing.T,
	ctx context.Context,
	sub outbox.Subscriber,
	topic string,
	n int,
	timeout time.Duration,
) []outbox.Entry {
	t.Helper()

	var (
		mu        sync.Mutex
		collected []outbox.Entry
		done      = make(chan struct{})
	)

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	handler := func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
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
	}

	// Subscribe blocks — run in goroutine.
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, handler)
	}()

	// Wait for subscription to register (InMemoryEventBus needs a brief delay).
	time.Sleep(20 * time.Millisecond)

	select {
	case <-done:
	case <-time.After(timeout):
		mu.Lock()
		got := len(collected)
		mu.Unlock()
		t.Fatalf("CollectN: timed out after %v, collected %d/%d entries", timeout, got, n)
	}

	cancel()
	<-subDone

	mu.Lock()
	defer mu.Unlock()
	return collected
}

// collectWithHandler subscribes and collects entries using a custom handler.
// Returns after either n entries are collected or timeout elapses.
func collectWithHandler(
	t *testing.T,
	ctx context.Context,
	sub outbox.Subscriber,
	topic string,
	n int,
	timeout time.Duration,
	handler outbox.EntryHandler,
) []outbox.Entry {
	t.Helper()

	var (
		mu        sync.Mutex
		collected []outbox.Entry
		done      = make(chan struct{})
	)

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	wrappedHandler := func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		res := handler(ctx, entry)

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
		return res
	}

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, wrappedHandler)
	}()

	time.Sleep(20 * time.Millisecond)

	select {
	case <-done:
	case <-time.After(timeout):
		// Not a fatal error for this helper — caller decides.
	}

	cancel()
	<-subDone

	mu.Lock()
	defer mu.Unlock()
	return collected
}
