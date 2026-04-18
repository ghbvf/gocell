package outbox_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// TestMarshalEnvelope_StampsSchemaVersionV1 verifies that MarshalEnvelope
// always stamps "schemaVersion":"v1" in the wire JSON.
func TestMarshalEnvelope_StampsSchemaVersionV1(t *testing.T) {
	entry := outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:        "stamp-id-1",
			EventType: "test.event.v1",
			Topic:     "test.event.v1",
			Payload:   []byte(`{"key":"value"}`),
			CreatedAt: time.Now(),
		},
	}

	raw, err := outbox.MarshalEnvelope(entry)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal wire JSON: %v", err)
	}

	sv, ok := m["schemaVersion"]
	if !ok {
		t.Fatal("schemaVersion field must be present in wire JSON")
	}
	var svStr string
	if err := json.Unmarshal(sv, &svStr); err != nil {
		t.Fatalf("schemaVersion must be a string: %v", err)
	}
	if svStr != "v1" {
		t.Errorf("schemaVersion: got %q, want %q", svStr, "v1")
	}
}

// TestUnmarshalEnvelope_V1_Success verifies that a well-formed v1 envelope
// is decoded correctly with all fields preserved.
func TestUnmarshalEnvelope_V1_Success(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	entry := outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:            "v1-id-1",
			AggregateID:   "agg-100",
			AggregateType: "Order",
			EventType:     "order.created.v1",
			Topic:         "order.created.v1",
			Payload:       []byte(`{"orderId":"o-1","amount":99}`),
			Metadata:      map[string]string{"trace_id": "t-123"},
			CreatedAt:     now,
		},
		Attempts: 1,
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
	if !got.CreatedAt.Equal(entry.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, entry.CreatedAt)
	}
}

// TestUnmarshalEnvelope_EmptyVersion_Rejected verifies that an envelope with no
// schemaVersion field is rejected with ErrUnknownEnvelopeVersion.
func TestUnmarshalEnvelope_EmptyVersion_Rejected(t *testing.T) {
	raw := []byte(`{"id":"x","eventType":"foo.v1","payload":{"data":"y"},"createdAt":"2024-01-01T00:00:00Z"}`)

	_, err := outbox.UnmarshalEnvelope("foo.v1", raw)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, outbox.ErrUnknownEnvelopeVersion) {
		t.Errorf("expected ErrUnknownEnvelopeVersion, got: %v", err)
	}
}

// TestUnmarshalEnvelope_FutureVersion_Rejected verifies that an unknown
// schemaVersion (e.g., "v99") is rejected with ErrUnknownEnvelopeVersion.
func TestUnmarshalEnvelope_FutureVersion_Rejected(t *testing.T) {
	raw := []byte(`{"schemaVersion":"v99","id":"x","eventType":"foo.v1","payload":{"data":"y"},"createdAt":"2024-01-01T00:00:00Z"}`)

	_, err := outbox.UnmarshalEnvelope("foo.v1", raw)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, outbox.ErrUnknownEnvelopeVersion) {
		t.Errorf("expected ErrUnknownEnvelopeVersion, got: %v", err)
	}
}

// TestUnmarshalEnvelope_BrokenJSON_Rejected verifies that invalid JSON bytes
// return an ErrEnvelopeSchema-coded error, but NOT the ErrUnknownEnvelopeVersion
// sentinel (broken JSON is distinct from a recognisable envelope with wrong version).
//
// Groups parse failures and schema failures under the same observability code
// (ERR_ENVELOPE_SCHEMA) while preserving errors.Is against the version sentinel
// for callers that need to distinguish the two cases.
func TestUnmarshalEnvelope_BrokenJSON_Rejected(t *testing.T) {
	raw := []byte(`not json at all {{{`)

	_, err := outbox.UnmarshalEnvelope("some.topic", raw)
	if err == nil {
		t.Fatal("expected error for broken JSON, got nil")
	}
	// Must NOT be ErrUnknownEnvelopeVersion — it's a JSON parse error.
	if errors.Is(err, outbox.ErrUnknownEnvelopeVersion) {
		t.Error("broken JSON should produce a parse error, not ErrUnknownEnvelopeVersion")
	}
	// But MUST carry the ErrEnvelopeSchema code so observability / HTTP mapping
	// groups it with schema-version and missing-field failures.
	var ce *errcode.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected *errcode.Error, got: %T (%v)", err, err)
	}
	if ce.Code != errcode.ErrEnvelopeSchema {
		t.Errorf("code: got %q, want %q", ce.Code, errcode.ErrEnvelopeSchema)
	}
}

// TestUnmarshalEnvelope_MissingRequiredField_Rejected verifies that a v1 envelope
// with an empty ID or empty EventType is rejected.
func TestUnmarshalEnvelope_MissingRequiredField_Rejected(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "empty id",
			raw:  []byte(`{"schemaVersion":"v1","id":"","eventType":"foo.v1","payload":{"d":"y"},"createdAt":"2024-01-01T00:00:00Z"}`),
		},
		{
			name: "missing id field",
			raw:  []byte(`{"schemaVersion":"v1","eventType":"foo.v1","payload":{"d":"y"},"createdAt":"2024-01-01T00:00:00Z"}`),
		},
		{
			name: "empty eventType",
			raw:  []byte(`{"schemaVersion":"v1","id":"some-id","eventType":"","payload":{"d":"y"},"createdAt":"2024-01-01T00:00:00Z"}`),
		},
		{
			name: "missing eventType field",
			raw:  []byte(`{"schemaVersion":"v1","id":"some-id","payload":{"d":"y"},"createdAt":"2024-01-01T00:00:00Z"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := outbox.UnmarshalEnvelope("foo.v1", tt.raw)
			if err == nil {
				t.Fatalf("[%s] expected error for missing required field, got nil", tt.name)
			}
		})
	}
}

// TestMarshalEnvelope_WireFormat verifies that the JSON keys are camelCase
// (not PascalCase), matching what adapters/postgres/outbox_relay.go produces,
// and that schemaVersion is included.
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

	for _, key := range []string{"schemaVersion", "id", "aggregateId", "aggregateType", "eventType", "topic", "payload", "createdAt"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected camelCase key %q not found in wire JSON", key)
		}
	}
	// Verify no PascalCase keys leaked.
	for _, key := range []string{"ID", "AggregateID", "AggregateType", "EventType", "SchemaVersion"} {
		if _, ok := m[key]; ok {
			t.Errorf("unexpected PascalCase key %q found in wire JSON", key)
		}
	}
}

// TestMarshalDirectEnvelope_ProducesV1 verifies that direct-publish envelopes
// carry SchemaVersion=v1 and the supplied fields, and round-trip through
// UnmarshalEnvelope so the bus delivers the business payload unchanged.
func TestMarshalDirectEnvelope_ProducesV1(t *testing.T) {
	topic := "event.session.created.v1"
	id := "direct-id-1"
	payload := []byte(`{"sessionId":"s-1","userId":"u-42"}`)

	raw := outbox.MarshalDirectEnvelope(topic, topic, id, payload)

	got, err := outbox.UnmarshalEnvelope(topic, raw)
	if err != nil {
		t.Fatalf("UnmarshalEnvelope: %v", err)
	}

	if got.ID != id {
		t.Errorf("ID: got %q, want %q", got.ID, id)
	}
	if got.EventType != topic {
		t.Errorf("EventType: got %q, want %q", got.EventType, topic)
	}
	if got.Topic != topic {
		t.Errorf("Topic: got %q, want %q", got.Topic, topic)
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("Payload: got %s, want %s", got.Payload, payload)
	}
}

// TestMarshalDirectEnvelope_PanicsOnEmptyRequiredFields verifies fail-fast on
// programmer-error input (empty id / eventType). Follows stdlib Must-style
// convention — callers pass compile-time constants / NewEntryID(), so these
// inputs never arise at runtime and returning error would force unreachable
// handling at every call site.
func TestMarshalDirectEnvelope_PanicsOnEmptyRequiredFields(t *testing.T) {
	tests := []struct {
		name, topic, eventType, id string
	}{
		{"empty id", "t.v1", "t.v1", ""},
		{"empty eventType", "t.v1", "", "some-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic, got none")
				}
			}()
			_ = outbox.MarshalDirectEnvelope(tt.topic, tt.eventType, tt.id, []byte(`{}`))
		})
	}
}

// TestUnmarshalEnvelope_MetadataTransparent verifies metadata round-trip.
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
