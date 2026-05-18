package outbox

import (
	"encoding/json"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// EnvelopeSchemaV1 is the canonical schema version for outbox wire envelopes.
const EnvelopeSchemaV1 = "v1"

// ErrUnknownEnvelopeVersion is returned when a wire message carries an
// unrecognized or absent schemaVersion field.
var ErrUnknownEnvelopeVersion = errcode.New(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
	"outbox: unknown envelope schema version")

// WireMessage is the canonical wire envelope used by outbox relay and direct
// publisher paths across transports.
//
// ID-shaped fields use idutil.SafeID, whose UnmarshalJSON fail-closes on
// unsafe characters (CWE-117 log injection) and length-cap violation. See
// SAFEID-WIREMESSAGE-USAGE-01 archtest and ai-collab.md §"Hard 范本" 第 3 条
// string-typed concept funnel.
type WireMessage struct {
	SchemaVersion string                `json:"schemaVersion"`
	ID            idutil.SafeID         `json:"id"`
	AggregateID   idutil.SafeID         `json:"aggregateId,omitempty"`
	AggregateType idutil.SafeID         `json:"aggregateType,omitempty"`
	EventType     idutil.SafeID         `json:"eventType"`
	Topic         idutil.SafeID         `json:"topic,omitempty"`
	Payload       json.RawMessage       `json:"payload"`
	Metadata      map[string]string     `json:"metadata,omitempty"`
	Observability ObservabilityMetadata `json:"observability,omitempty"`
	CreatedAt     time.Time             `json:"createdAt"`
}

// MarshalEnvelope serializes an Entry into the canonical v1 wire envelope.
//
// Callers are responsible for setting entry.CreatedAt before calling
// MarshalEnvelope; this function is pure (no clock interaction). The PG
// outbox writer pre-fills CreatedAt from now() at INSERT time, the relay
// reads it back from the row, and DirectEmitter sets it from its injected
// clock.Clock — all paths populate CreatedAt before reaching this function.
//
// Producer-side fail-fast: every ID-shaped field is parsed through
// idutil.ParseSafeID so an unsafe in-memory Entry surfaces an error at
// write time rather than poisoning downstream consumers (defense in depth
// against accidental Entry{ID: rawUnsafe} construction outside the
// trusted MustNewEntryID path).
func MarshalEnvelope(entry Entry) ([]byte, error) {
	id, err := idutil.ParseSafeID(entry.ID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.ID", err)
	}
	aggID, err := idutil.ParseSafeID(entry.AggregateID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.AggregateID", err)
	}
	aggType, err := idutil.ParseSafeID(entry.AggregateType)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.AggregateType", err)
	}
	eventType, err := idutil.ParseSafeID(entry.EventType)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.EventType", err)
	}
	topic, err := idutil.ParseSafeID(entry.RoutingTopic())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.Topic", err)
	}
	// Producer-side observability fail-fast (PR #582 round-3 review F2/F3):
	// TraceParent is a `string` (W3C format, not SafeID) — without this
	// explicit revalidate, an unsafe TraceParent in entry.Observability
	// would slip past SafeID's UnmarshalJSON-driven funnel and reach wire.
	if err := entry.Observability.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: marshal envelope: invalid observability", err)
	}
	msg := WireMessage{
		SchemaVersion: EnvelopeSchemaV1,
		ID:            id,
		AggregateID:   aggID,
		AggregateType: aggType,
		EventType:     eventType,
		Topic:         topic,
		Payload:       json.RawMessage(entry.Payload),
		Metadata:      entry.Metadata,
		Observability: entry.Observability,
		CreatedAt:     entry.CreatedAt,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope", err)
	}
	return b, nil
}

// UnmarshalEnvelope decodes a v1 wire envelope into an Entry. Unsafe
// ID-shaped fields (newline, length overrun, etc.) are rejected during
// json.Unmarshal via idutil.SafeID.UnmarshalJSON — wrapped here as
// ErrEnvelopeSchema for consistent error classification across the
// schema-version / missing-field / unsafe-id rejection paths.
func UnmarshalEnvelope(topic string, raw []byte) (Entry, error) {
	var msg WireMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Entry{}, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: unmarshal envelope", err)
	}
	if msg.SchemaVersion != EnvelopeSchemaV1 {
		return Entry{}, ErrUnknownEnvelopeVersion
	}
	if msg.ID == "" {
		return Entry{}, errcode.New(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: envelope missing required field: id")
	}
	if msg.EventType == "" {
		return Entry{}, errcode.New(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: envelope missing required field: eventType")
	}
	// Wire-side observability fail-closed (PR #582 round-3 review F3):
	// SafeID.UnmarshalJSON covers ID-shaped fields automatically, but
	// TraceParent is `string` with W3C format validator (validTraceParent)
	// that only runs inside ObservabilityMetadata.Validate(). Without this
	// explicit call, an envelope with malformed traceparent flows through
	// to slog/trace consumers. Mirrors OpenTelemetry propagation.
	// TraceContext.Extract pattern (validate every field at decode time).
	if err := msg.Observability.Validate(); err != nil {
		return Entry{}, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: envelope observability invalid", err)
	}
	entryTopic := string(msg.Topic)
	if entryTopic == "" {
		entryTopic = topic
	}
	return Entry{
		ID:            string(msg.ID),
		AggregateID:   string(msg.AggregateID),
		AggregateType: string(msg.AggregateType),
		EventType:     string(msg.EventType),
		Topic:         entryTopic,
		Payload:       []byte(msg.Payload),
		Metadata:      msg.Metadata,
		Observability: msg.Observability,
		CreatedAt:     msg.CreatedAt,
	}, nil
}
