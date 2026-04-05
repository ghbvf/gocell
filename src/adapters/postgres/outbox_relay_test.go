package postgres

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time checks.
func TestOutboxRelay_CompileCheck(t *testing.T) {
	var _ outbox.Relay = (*OutboxRelay)(nil)
	var _ worker.Worker = (*OutboxRelay)(nil)
}

// spyPublisher records Publish calls for test assertions.
type spyPublisher struct {
	mu       sync.Mutex
	calls    []publishCall
	publishErr error
}

type publishCall struct {
	topic   string
	payload []byte
}

func (s *spyPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, publishCall{topic: topic, payload: payload})
	if s.publishErr != nil {
		return s.publishErr
	}
	return nil
}

func (s *spyPublisher) getCalls() []publishCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]publishCall, len(s.calls))
	copy(cp, s.calls)
	return cp
}

var _ outbox.Publisher = (*spyPublisher)(nil)

func TestDefaultRelayConfig(t *testing.T) {
	cfg := DefaultRelayConfig()
	assert.Equal(t, 1*time.Second, cfg.PollInterval)
	assert.Equal(t, 100, cfg.BatchSize)
	assert.Equal(t, 72*time.Hour, cfg.RetentionPeriod)
	assert.Equal(t, 1*time.Hour, cfg.CleanupInterval)
}

func TestNewOutboxRelay_DefaultsZeroValues(t *testing.T) {
	pub := &spyPublisher{}
	relay := NewOutboxRelay(nil, pub, RelayConfig{})

	assert.Equal(t, 1*time.Second, relay.cfg.PollInterval)
	assert.Equal(t, 100, relay.cfg.BatchSize)
	assert.Equal(t, 72*time.Hour, relay.cfg.RetentionPeriod)
	assert.Equal(t, 1*time.Hour, relay.cfg.CleanupInterval)
}

func TestNewOutboxRelay_CustomConfig(t *testing.T) {
	pub := &spyPublisher{}
	cfg := RelayConfig{
		PollInterval:    500 * time.Millisecond,
		BatchSize:       50,
		RetentionPeriod: 24 * time.Hour,
		CleanupInterval: 30 * time.Minute,
	}
	relay := NewOutboxRelay(nil, pub, cfg)

	assert.Equal(t, 500*time.Millisecond, relay.cfg.PollInterval)
	assert.Equal(t, 50, relay.cfg.BatchSize)
	assert.Equal(t, 24*time.Hour, relay.cfg.RetentionPeriod)
	assert.Equal(t, 30*time.Minute, relay.cfg.CleanupInterval)
}

func TestOutboxRelay_StopBeforeStart(t *testing.T) {
	pub := &spyPublisher{}
	relay := NewOutboxRelay(nil, pub, DefaultRelayConfig())

	// Stop before Start should not panic or block.
	err := relay.Stop(context.Background())
	require.NoError(t, err)
}

func TestOutboxRelay_StopIdempotent(t *testing.T) {
	pub := &spyPublisher{}
	relay := NewOutboxRelay(nil, pub, DefaultRelayConfig())

	// Multiple stops should not panic.
	require.NoError(t, relay.Stop(context.Background()))
	require.NoError(t, relay.Stop(context.Background()))
}

func TestOutboxRelay_StartStopLifecycle(t *testing.T) {
	pub := &spyPublisher{}
	// Use a long poll interval so we don't actually poll during this test.
	cfg := RelayConfig{
		PollInterval:    10 * time.Second,
		BatchSize:       100,
		RetentionPeriod: 72 * time.Hour,
		CleanupInterval: 10 * time.Second,
	}
	relay := NewOutboxRelay(nil, pub, cfg)

	done := make(chan error, 1)
	go func() {
		done <- relay.Start(context.Background())
	}()

	// Give Start a moment to spin up goroutines.
	time.Sleep(50 * time.Millisecond)

	err := relay.Stop(context.Background())
	require.NoError(t, err)

	select {
	case startErr := <-done:
		require.NoError(t, startErr)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestOutboxRelay_ContextCancellation(t *testing.T) {
	pub := &spyPublisher{}
	cfg := RelayConfig{
		PollInterval:    10 * time.Second,
		BatchSize:       100,
		RetentionPeriod: 72 * time.Hour,
		CleanupInterval: 10 * time.Second,
	}
	relay := NewOutboxRelay(nil, pub, cfg)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- relay.Start(ctx)
	}()

	// Give Start a moment to spin up.
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestOutboxRelay_SerializeEntry(t *testing.T) {
	pub := &spyPublisher{}
	relay := NewOutboxRelay(nil, pub, DefaultRelayConfig())
	now := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)

	entry := outbox.Entry{
		ID:            "e-001",
		AggregateID:   "agg-001",
		AggregateType: "order",
		EventType:     "order.created",
		Payload:       []byte(`{"amount":100}`),
		Metadata:      map[string]string{"source": "test"},
		CreatedAt:     now,
	}

	data, err := relay.serializeEntry(&entry)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))

	assert.Equal(t, "e-001", result["id"])
	assert.Equal(t, "agg-001", result["aggregateId"])
	assert.Equal(t, "order", result["aggregateType"])
	assert.Equal(t, "order.created", result["eventType"])
	assert.Equal(t, "test", result["metadata"].(map[string]any)["source"])

	// Payload should be embedded as nested JSON, not a string.
	payloadMap, ok := result["payload"].(map[string]any)
	require.True(t, ok, "payload should be a JSON object, got %T", result["payload"])
	assert.Equal(t, float64(100), payloadMap["amount"])
}

func TestOutboxRelay_SerializeEntry_NilMetadata(t *testing.T) {
	pub := &spyPublisher{}
	relay := NewOutboxRelay(nil, pub, DefaultRelayConfig())

	entry := outbox.Entry{
		ID:        "e-002",
		EventType: "test.event",
		Payload:   []byte(`{}`),
		Metadata:  nil,
		CreatedAt: time.Now(),
	}

	data, err := relay.serializeEntry(&entry)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, "e-002", result["id"])
	// nil metadata should marshal as null.
	assert.Nil(t, result["metadata"])
}
