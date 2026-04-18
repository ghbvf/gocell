package outbox_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/outbox"
)

func TestMarshalUnmarshalEnvelope_RoundTrip(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	entry := outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:            "test-id-1",
			AggregateID:   "agg-123",
			AggregateType: "Order",
			EventType:     "order.created.v1",
			Topic:         "order.created.v1",
			Payload:       []byte(`{"orderId":"123","amount":99}`),
			Metadata:      map[string]string{"trace_id": "abc123", "request_id": "req-456"},
			CreatedAt:     now,
		},
		Attempts: 2,
	}

	raw, err := outbox.MarshalEnvelope(entry)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}

	got, err := outbox.UnmarshalEnvelope(entry.Topic, raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope: %v", err)
	}

	if got.ID != entry.ID {
		t.Errorf("ID: got %q, want %q", got.ID, entry.ID)
	}
	if got.AggregateID != entry.AggregateID {
		t.Errorf("AggregateID: got %q, want %q", got.AggregateID, entry.AggregateID)
	}
	if got.AggregateType != entry.AggregateType {
		t.Errorf("AggregateType: got %q, want %q", got.AggregateType, entry.AggregateType)
	}
	if got.EventType != entry.EventType {
		t.Errorf("EventType: got %q, want %q", got.EventType, entry.EventType)
	}
	if string(got.Payload) != string(entry.Payload) {
		t.Errorf("Payload: got %s, want %s", got.Payload, entry.Payload)
	}
	if got.Metadata["trace_id"] != entry.Metadata["trace_id"] {
		t.Errorf("Metadata trace_id: got %q, want %q", got.Metadata["trace_id"], entry.Metadata["trace_id"])
	}
	if got.Metadata["request_id"] != entry.Metadata["request_id"] {
		t.Errorf("Metadata request_id: got %q, want %q", got.Metadata["request_id"], entry.Metadata["request_id"])
	}
	if !got.CreatedAt.Equal(entry.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, entry.CreatedAt)
	}
}

func TestUnmarshalEnvelope_Fallback_NonEnvelope(t *testing.T) {
	topic := "some.topic.v1"
	raw := []byte(`{"foo":"bar","baz":42}`)

	// Non-envelope payload (no "id" or "eventType" fields) → fresh entry fallback.
	got, err := outbox.UnmarshalEnvelope(topic, raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope unexpected error: %v", err)
	}
	if !strings.HasPrefix(got.ID, "evt-") {
		t.Errorf("ID should start with 'evt-', got %q", got.ID)
	}
	if got.EventType != topic {
		t.Errorf("EventType: got %q, want %q", got.EventType, topic)
	}
	if string(got.Payload) != string(raw) {
		t.Errorf("Payload: got %s, want %s", got.Payload, raw)
	}
}

func TestUnmarshalEnvelope_Fallback_BrokenJSON(t *testing.T) {
	topic := "some.topic.v1"
	raw := []byte(`not json`)

	// Broken JSON → fallback (not an error in eventbus path; raw is preserved).
	got, err := outbox.UnmarshalEnvelope(topic, raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope unexpected error: %v", err)
	}
	if !strings.HasPrefix(got.ID, "evt-") {
		t.Errorf("ID should start with 'evt-', got %q", got.ID)
	}
	if got.EventType != topic {
		t.Errorf("EventType: got %q, want %q", got.EventType, topic)
	}
	if string(got.Payload) != string(raw) {
		t.Errorf("Payload: got %s, want %s", got.Payload, raw)
	}
}

func TestUnmarshalEnvelope_Fallback_EmptyPayload(t *testing.T) {
	topic := "some.topic.v1"
	var raw []byte

	got, err := outbox.UnmarshalEnvelope(topic, raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope unexpected error: %v", err)
	}
	if !strings.HasPrefix(got.ID, "evt-") {
		t.Errorf("ID should start with 'evt-', got %q", got.ID)
	}
}

func TestUnmarshalEnvelope_Fallback_MissingPayloadField(t *testing.T) {
	// Envelope-like but missing the embedded "payload" field → fallback.
	raw := []byte(`{"id":"test-id","eventType":"order.v1"}`)

	got, err := outbox.UnmarshalEnvelope("order.v1", raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope unexpected error: %v", err)
	}
	// No payload field → json.RawMessage is nil → isEmbeddedJSON returns false → fallback.
	if !strings.HasPrefix(got.ID, "evt-") {
		t.Errorf("ID should start with 'evt-', got %q", got.ID)
	}
}

func TestUnmarshalEnvelope_Fallback_PayloadNotEmbeddedJSON(t *testing.T) {
	// Envelope with payload that is a string (not an object/array) → fallback
	// because isEmbeddedJSON("not embedded") == false.
	raw := []byte(`{"id":"test-id","eventType":"order.v1","payload":"not embedded"}`)

	got, err := outbox.UnmarshalEnvelope("order.v1", raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope unexpected error: %v", err)
	}
	if !strings.HasPrefix(got.ID, "evt-") {
		t.Errorf("ID should start with 'evt-', got %q", got.ID)
	}
}

func TestUnmarshalEnvelope_Fallback_PayloadIsArray(t *testing.T) {
	// Envelope with payload that is a JSON array → detected as envelope (array is embedded JSON).
	raw := []byte(`{"id":"arr-id","eventType":"list.v1","payload":[1,2,3]}`)

	got, err := outbox.UnmarshalEnvelope("list.v1", raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope unexpected error: %v", err)
	}
	// Should be detected as envelope.
	if got.ID != "arr-id" {
		t.Errorf("ID: got %q, want %q", got.ID, "arr-id")
	}
	if string(got.Payload) != "[1,2,3]" {
		t.Errorf("Payload: got %s, want [1,2,3]", got.Payload)
	}
}

func TestUnmarshalEnvelope_MetadataTransparent(t *testing.T) {
	meta := map[string]string{
		"trace_id":       "t-001",
		"correlation_id": "c-999",
		"custom_key":     "custom_value",
	}
	entry := outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:        "meta-test-id",
			EventType: "meta.event.v1",
			Topic:     "meta.event.v1",
			Payload:   []byte(`{"data":"value"}`),
			Metadata:  meta,
			CreatedAt: time.Now(),
		},
	}

	raw, err := outbox.MarshalEnvelope(entry)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}

	got, err := outbox.UnmarshalEnvelope(entry.Topic, raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope: %v", err)
	}

	for k, v := range meta {
		if got.Metadata[k] != v {
			t.Errorf("Metadata[%q]: got %q, want %q", k, got.Metadata[k], v)
		}
	}
	if len(got.Metadata) != len(meta) {
		t.Errorf("Metadata len: got %d, want %d", len(got.Metadata), len(meta))
	}
}

// TestMarshalEnvelope_WireFormat verifies that the JSON keys are camelCase
// (not PascalCase), matching what adapters/postgres/outbox_relay.go produces.
func TestMarshalEnvelope_WireFormat(t *testing.T) {
	entry := outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:            "wire-id",
			AggregateID:   "agg-1",
			AggregateType: "Widget",
			EventType:     "widget.created.v1",
			Topic:         "widget.created.v1",
			Payload:       []byte(`{"widgetId":"w-1"}`),
			CreatedAt:     time.Now(),
		},
		Attempts: 3,
	}

	raw, err := outbox.MarshalEnvelope(entry)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal wire JSON: %v", err)
	}

	for _, key := range []string{"id", "aggregateId", "aggregateType", "eventType", "topic", "payload", "createdAt"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected camelCase key %q not found in wire JSON", key)
		}
	}
	// Verify no PascalCase keys leaked.
	for _, key := range []string{"ID", "AggregateID", "AggregateType", "EventType"} {
		if _, ok := m[key]; ok {
			t.Errorf("unexpected PascalCase key %q found in wire JSON", key)
		}
	}
}
