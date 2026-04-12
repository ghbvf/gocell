package outboxtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestTopic returns a unique topic name scoped to the given test.
// Prevents cross-test interference when implementations share state.
func TestTopic(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("outboxtest: crypto/rand failed: %v", err)
	}
	return fmt.Sprintf("test-%s-%s", t.Name(), hex.EncodeToString(b))
}

// NewEntry creates a valid Entry with a unique ID for testing.
func NewEntry(topic string, payload []byte) outbox.Entry {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return outbox.Entry{
		ID:        "evt-" + hex.EncodeToString(b),
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
		if err := pub.Publish(ctx, topic, payload); err != nil {
			t.Fatalf("publish message %d: %v", i, err)
		}
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
	time.Sleep(subscribeInitDelay)

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

// ---------------------------------------------------------------------------
// Internal assertion helpers — stdlib only, no testify in kernel/ non-test files
// ---------------------------------------------------------------------------

func assertNoError(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	if err != nil {
		if len(msgAndArgs) > 0 {
			t.Fatalf("unexpected error: %v — %s", err, fmt.Sprint(msgAndArgs...))
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertEqual(t *testing.T, want, got any, msgAndArgs ...any) {
	t.Helper()
	if fmt.Sprintf("%v", want) != fmt.Sprintf("%v", got) {
		suffix := ""
		if len(msgAndArgs) > 0 {
			suffix = " — " + fmt.Sprint(msgAndArgs...)
		}
		t.Fatalf("want %v, got %v%s", want, got, suffix)
	}
}

func assertBytesEqual(t *testing.T, want, got []byte, msgAndArgs ...any) {
	t.Helper()
	if string(want) != string(got) {
		suffix := ""
		if len(msgAndArgs) > 0 {
			suffix = " — " + fmt.Sprint(msgAndArgs...)
		}
		t.Fatalf("want %q, got %q%s", want, got, suffix)
	}
}

func assertTrue(t *testing.T, condition bool, msgAndArgs ...any) {
	t.Helper()
	if !condition {
		if len(msgAndArgs) > 0 {
			t.Fatalf("expected true: %s", fmt.Sprint(msgAndArgs...))
		}
		t.Fatal("expected true")
	}
}

func assertFalse(t *testing.T, condition bool, msgAndArgs ...any) {
	t.Helper()
	if condition {
		if len(msgAndArgs) > 0 {
			t.Fatalf("expected false: %s", fmt.Sprint(msgAndArgs...))
		}
		t.Fatal("expected false")
	}
}

func assertLen(t *testing.T, length, want int, msgAndArgs ...any) {
	t.Helper()
	if length != want {
		if len(msgAndArgs) > 0 {
			t.Fatalf("want length %d, got %d — %s", want, length, fmt.Sprint(msgAndArgs...))
		}
		t.Fatalf("want length %d, got %d", want, length)
	}
}

func assertEventually(t *testing.T, condition func() bool, timeout, poll time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(poll)
	}
	t.Fatalf("condition not met within %v: %s", timeout, msg)
}

func assertNotPanics(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	f()
}
