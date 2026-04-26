package outbox

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalEnvelope_StampsSchemaVersionV1(t *testing.T) {
	entry := Entry{
		ID:        "stamp-id-1",
		EventType: "test.event.v1",
		Topic:     "test.event.v1",
		Payload:   []byte(`{"key":"value"}`),
		CreatedAt: time.Now(),
	}

	raw, err := MarshalEnvelope(entry)
	require.NoError(t, err)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))

	var got string
	require.NoError(t, json.Unmarshal(m["schemaVersion"], &got))
	assert.Equal(t, EnvelopeSchemaV1, got)
}

func TestUnmarshalEnvelope_V1Success(t *testing.T) {
	now := time.Date(2026, 4, 23, 12, 30, 0, 0, time.UTC)
	entry := Entry{
		ID:            "v1-id-1",
		AggregateID:   "agg-100",
		AggregateType: "Order",
		EventType:     "order.created.v1",
		Topic:         "order.created.v1",
		Payload:       []byte(`{"orderId":"o-1","amount":99}`),
		Metadata:      map[string]string{"trace_id": "t-123"},
		Observability: ObservabilityMetadata{TraceID: "abc123"},
		CreatedAt:     now,
	}

	raw, err := MarshalEnvelope(entry)
	require.NoError(t, err)

	got, err := UnmarshalEnvelope(entry.Topic, raw)
	require.NoError(t, err)

	assert.Equal(t, entry.ID, got.ID)
	assert.Equal(t, entry.AggregateID, got.AggregateID)
	assert.Equal(t, entry.AggregateType, got.AggregateType)
	assert.Equal(t, entry.EventType, got.EventType)
	assert.Equal(t, entry.Topic, got.Topic)
	assert.Equal(t, string(entry.Payload), string(got.Payload))
	assert.Equal(t, entry.Metadata, got.Metadata)
	assert.True(t, got.CreatedAt.Equal(now))
	assert.Equal(t, entry.Observability, got.Observability)
}

func TestUnmarshalEnvelope_UnknownVersionRejected(t *testing.T) {
	raw := []byte(`{"schemaVersion":"v99","id":"x","eventType":"foo.v1","payload":{"data":"y"},"createdAt":"2026-04-23T00:00:00Z"}`)

	_, err := UnmarshalEnvelope("foo.v1", raw)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownEnvelopeVersion)
}

func TestUnmarshalEnvelope_BrokenJSONRejected(t *testing.T) {
	_, err := UnmarshalEnvelope("some.topic", []byte(`not json at all {{{`))

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnknownEnvelopeVersion)

	var ce *errcode.Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, errcode.ErrEnvelopeSchema, ce.Code)
}

func TestUnmarshalEnvelope_MissingRequiredFieldRejected(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "empty id",
			raw:  []byte(`{"schemaVersion":"v1","id":"","eventType":"foo.v1","payload":{"d":"y"},"createdAt":"2026-04-23T00:00:00Z"}`),
		},
		{
			name: "empty eventType",
			raw:  []byte(`{"schemaVersion":"v1","id":"some-id","eventType":"","payload":{"d":"y"},"createdAt":"2026-04-23T00:00:00Z"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UnmarshalEnvelope("foo.v1", tt.raw)
			assert.Error(t, err)
		})
	}
}

func TestMarshalDirectEnvelope_ProducesV1(t *testing.T) {
	topic := "event.session.created.v1"
	id := "direct-id-1"
	payload := []byte(`{"sessionId":"s-1","userId":"u-42"}`)

	raw := MarshalDirectEnvelope(topic, topic, id, payload)

	got, err := UnmarshalEnvelope(topic, raw)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, topic, got.EventType)
	assert.Equal(t, topic, got.Topic)
	assert.Equal(t, string(payload), string(got.Payload))
}

func TestUnmarshalEnvelope_PreservesObservability(t *testing.T) {
	// W3C traceparent: version(2)-traceID(32)-spanID(16)-flags(2) = 55 bytes, lowercase hex.
	obs := ObservabilityMetadata{
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		TraceParent:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		RequestID:     "req-abc-123",
		CorrelationID: "corr-xyz-456",
	}

	entry := Entry{
		ID:            "obs-rt-1",
		EventType:     "test.event.v1",
		Topic:         "test.event.v1",
		Payload:       []byte(`{"x":1}`),
		Metadata:      map[string]string{"foo": "bar"},
		Observability: obs,
		CreatedAt:     time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
	}

	raw, err := MarshalEnvelope(entry)
	require.NoError(t, err)

	got, err := UnmarshalEnvelope(entry.Topic, raw)
	require.NoError(t, err)

	assert.Equal(t, obs.TraceID, got.Observability.TraceID)
	assert.Equal(t, obs.TraceParent, got.Observability.TraceParent)
	assert.Equal(t, obs.RequestID, got.Observability.RequestID)
	assert.Equal(t, obs.CorrelationID, got.Observability.CorrelationID)
	// struct-equal 兜底：未来新增字段时测试自动失败
	assert.Equal(t, obs, got.Observability)
}

func TestMarshalDirectEnvelope_PanicsOnEmptyRequiredFields(t *testing.T) {
	tests := []struct {
		name      string
		topic     string
		eventType string
		id        string
	}{
		{name: "empty id", topic: "t.v1", eventType: "t.v1", id: ""},
		{name: "empty eventType", topic: "t.v1", eventType: "", id: "some-id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() {
				_ = MarshalDirectEnvelope(tt.topic, tt.eventType, tt.id, []byte(`{}`))
			})
		})
	}
}
