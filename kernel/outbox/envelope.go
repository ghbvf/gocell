package outbox

import (
	"encoding/json"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// EnvelopeSchemaV1 is the canonical schema version for outbox wire envelopes.
const EnvelopeSchemaV1 = "v1"

// ErrUnknownEnvelopeVersion is returned when a wire message carries an
// unrecognized or absent schemaVersion field.
var ErrUnknownEnvelopeVersion = errcode.New(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
	"outbox: unknown envelope schema version")

// WireMessage is the canonical wire envelope used by outbox relay and direct
// publisher paths across transports.
type WireMessage struct {
	SchemaVersion string                `json:"schemaVersion"`
	ID            string                `json:"id"`
	AggregateID   string                `json:"aggregateId,omitempty"`
	AggregateType string                `json:"aggregateType,omitempty"`
	EventType     string                `json:"eventType"`
	Topic         string                `json:"topic,omitempty"`
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
func MarshalEnvelope(entry Entry) ([]byte, error) {
	msg := WireMessage{
		SchemaVersion: EnvelopeSchemaV1,
		ID:            entry.ID,
		AggregateID:   entry.AggregateID,
		AggregateType: entry.AggregateType,
		EventType:     entry.EventType,
		Topic:         entry.RoutingTopic(),
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

// UnmarshalEnvelope decodes a v1 wire envelope into an Entry.
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
	entryTopic := msg.Topic
	if entryTopic == "" {
		entryTopic = topic
	}
	return Entry{
		ID:            msg.ID,
		AggregateID:   msg.AggregateID,
		AggregateType: msg.AggregateType,
		EventType:     msg.EventType,
		Topic:         entryTopic,
		Payload:       []byte(msg.Payload),
		Metadata:      msg.Metadata,
		Observability: msg.Observability,
		CreatedAt:     msg.CreatedAt,
	}, nil
}
