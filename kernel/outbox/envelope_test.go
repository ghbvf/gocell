package outbox

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
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
		// PR246-FU1 reserved keys (trace_id/request_id/...) belong in Observability, not Metadata.
		Metadata:      map[string]string{"source": "test"},
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
		{
			// User-flagged finding: wire envelope without payload field used to
			// flow through UnmarshalEnvelope and be dispatched to handlers with
			// an empty Entry.Payload. wire boundary must reject.
			name: "missing payload field",
			raw:  []byte(`{"schemaVersion":"v1","id":"some-id","eventType":"foo.v1","createdAt":"2026-04-23T00:00:00Z"}`),
		},
		{
			name: "null payload",
			raw:  []byte(`{"schemaVersion":"v1","id":"some-id","eventType":"foo.v1","payload":null,"createdAt":"2026-04-23T00:00:00Z"}`),
		},
		{
			name: "empty payload object",
			raw:  []byte(`{"schemaVersion":"v1","id":"some-id","eventType":"foo.v1","payload":,"createdAt":"2026-04-23T00:00:00Z"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UnmarshalEnvelope("foo.v1", tt.raw)
			require.Error(t, err)
			var ce *errcode.Error
			require.True(t, errors.As(err, &ce))
			assert.Equal(t, errcode.ErrEnvelopeSchema, ce.Code,
				"all required-field rejections must surface as ErrEnvelopeSchema")
		})
	}
}

func TestMarshalEnvelope_ProducesV1FromMinimalEntry(t *testing.T) {
	topic := "event.session.created.v1"
	id := "direct-id-1"
	payload := []byte(`{"sessionId":"s-1","userId":"u-42"}`)

	raw, err := MarshalEnvelope(Entry{
		ID:        id,
		EventType: topic,
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

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

func TestUnmarshalEnvelope_RejectsUnsafeIDs(t *testing.T) {
	// CWE-117 log-injection trust boundary: every ID-shaped wire field
	// must pass idutil.IsSafeID + length cap at decode time. Required-empty
	// checks remain separate (covered by TestUnmarshalEnvelope_MissingRequiredFieldRejected).
	envelope := func(overrides map[string]string) []byte {
		fields := map[string]string{
			"schemaVersion": "v1",
			"id":            "ok",
			"eventType":     "foo.v1",
		}
		for k, v := range overrides {
			fields[k] = v
		}
		var parts []string
		for k, v := range fields {
			parts = append(parts, `"`+k+`":"`+v+`"`)
		}
		body := strings.Join(parts, ",")
		return []byte(`{` + body + `,"payload":{"d":1},"createdAt":"2026-04-23T00:00:00Z"}`)
	}
	tests := []struct {
		name      string
		overrides map[string]string
	}{
		{name: "ID with newline injection", overrides: map[string]string{"id": `evt-1\nlevel=error msg=injected`}},
		{name: "ID with CR injection", overrides: map[string]string{"id": `evt-1\rINJECT`}},
		{name: "ID overlong", overrides: map[string]string{"id": strings.Repeat("a", 257)}},
		{name: "EventType with space", overrides: map[string]string{"eventType": "foo v1"}},
		{name: "Topic with newline", overrides: map[string]string{"topic": `foo.v1\nx`}},
		{name: "AggregateID with angle brackets", overrides: map[string]string{"aggregateId": "<script>"}},
		{name: "AggregateType with space", overrides: map[string]string{"aggregateType": "some type"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UnmarshalEnvelope("foo.v1", envelope(tt.overrides))
			require.Error(t, err, "unsafe ID-shaped field must fail-closed at wire boundary")
			var ce *errcode.Error
			require.True(t, errors.As(err, &ce))
			assert.Equal(t, errcode.ErrEnvelopeSchema, ce.Code,
				"wire-boundary validation failures must surface as ErrEnvelopeSchema")
			// Explicit assertion that rejection came from SafeID.UnmarshalJSON
			// (not from a JSON parse error). Without this, future regressions
			// that loosen IsSafeID could still pass by accident if JSON parsing
			// happens to fail for unrelated reasons.
			assert.Contains(t, err.Error(), "idutil: SafeID",
				"rejection must originate from idutil.SafeID.UnmarshalJSON")
		})
	}
}

// TestUnmarshalEnvelope_RejectsNullID covers the JSON null path for an
// ID-shaped field embedded in a struct. SafeID.UnmarshalJSON treats null
// as zero-value (no error); Entry.Validate then rejects the empty ID.
func TestUnmarshalEnvelope_RejectsNullID(t *testing.T) {
	raw := []byte(`{"schemaVersion":"v1","id":null,"eventType":"foo.v1","payload":{"d":1},"createdAt":"2026-04-23T00:00:00Z"}`)
	_, err := UnmarshalEnvelope("foo.v1", raw)
	require.Error(t, err)
	var ce *errcode.Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, errcode.ErrEnvelopeSchema, ce.Code)
	assert.Contains(t, err.Error(), "missing ID")
}

func TestMarshalEnvelope_RejectsUnsafeIDs(t *testing.T) {
	// Producer-side fail-fast: MarshalEnvelope calls idutil.ParseSafeID on
	// all 5 ID-shaped fields. An unsafe in-memory Entry (e.g. accidental
	// SafeID(rawUnsafe) cast) must be caught at write time rather than
	// poisoning downstream consumers (defense in depth).
	validEntry := Entry{
		ID:            "valid-id",
		AggregateID:   "agg-1",
		AggregateType: "Order",
		EventType:     "order.created.v1",
		Topic:         "order.created.v1",
		Payload:       []byte(`{"x":1}`),
		CreatedAt:     time.Now(),
	}

	tests := []struct {
		name  string
		entry Entry
	}{
		{
			name:  "ID with newline injection",
			entry: func() Entry { e := validEntry; e.ID = "evt-1\nlevel=error"; return e }(),
		},
		{
			name:  "AggregateID with angle brackets",
			entry: func() Entry { e := validEntry; e.AggregateID = "<script>"; return e }(),
		},
		{
			name:  "AggregateType with space",
			entry: func() Entry { e := validEntry; e.AggregateType = "some type"; return e }(),
		},
		{
			name:  "EventType with newline",
			entry: func() Entry { e := validEntry; e.EventType = "foo.v1\nx"; return e }(),
		},
		{
			name:  "Topic with CR injection",
			entry: func() Entry { e := validEntry; e.Topic = "foo.v1\rINJECT"; return e }(),
		},
		{
			name: "invalid TraceParent in Observability",
			entry: func() Entry {
				e := validEntry
				e.Observability = ObservabilityMetadata{TraceParent: "malformed"}
				return e
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MarshalEnvelope(tt.entry)
			require.Error(t, err, "unsafe ID-shaped field must be rejected at marshal time")
			var ce *errcode.Error
			require.True(t, errors.As(err, &ce))
			assert.Equal(t, errcode.ErrEnvelopeSchema, ce.Code,
				"producer-side validation failures must surface as ErrEnvelopeSchema")
		})
	}
}

// TestEntryValidate_RejectsUnsafeIDFields covers validateEntryIDField defense
// in depth: every ID-shaped Entry field (5 total) must reject unsafe chars
// even in pure in-memory construction (no wire involvement).
func TestEntryValidate_RejectsUnsafeIDFields(t *testing.T) {
	base := Entry{
		ID: "valid", EventType: "t.v1", Topic: "t.v1", Payload: []byte(`{}`),
	}
	tests := []struct {
		name  string
		mut   func(*Entry)
		field string
	}{
		{name: "unsafe ID", mut: func(e *Entry) { e.ID = "id\nbad" }, field: "id"},
		{name: "unsafe EventType", mut: func(e *Entry) { e.EventType = "t\nv1"; e.Topic = "t.v1" }, field: "eventType"},
		{name: "unsafe Topic", mut: func(e *Entry) { e.Topic = "t\nv1"; e.EventType = "t.v1" }, field: "topic"},
		{name: "unsafe AggregateID", mut: func(e *Entry) { e.AggregateID = "<script>" }, field: "aggregateId"},
		{name: "unsafe AggregateType", mut: func(e *Entry) { e.AggregateType = "a b" }, field: "aggregateType"},
		{name: "overlong ID", mut: func(e *Entry) { e.ID = strings.Repeat("a", 257) }, field: "id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := base
			tt.mut(&e)
			err := e.Validate()
			require.Error(t, err, "Entry.Validate must reject unsafe %s", tt.field)
			var ce *errcode.Error
			require.True(t, errors.As(err, &ce))
			assert.Equal(t, errcode.ErrValidationFailed, ce.Code)
		})
	}
}

func TestEntryValidate_RejectsEmptyRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		e    Entry
	}{
		{name: "empty id", e: Entry{EventType: "t.v1", Topic: "t.v1", Payload: []byte(`{}`)}},
		{name: "empty eventType and topic", e: Entry{ID: "some-id", Payload: []byte(`{}`)}},
		{name: "empty payload", e: Entry{ID: "some-id", EventType: "t.v1", Topic: "t.v1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, tt.e.Validate())
		})
	}
}
