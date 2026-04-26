package outbox

import (
	"encoding/json"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// EnvelopeSchemaV1 is the canonical schema version for outbox wire envelopes.
const EnvelopeSchemaV1 = "v1"

// ErrUnknownEnvelopeVersion is returned when a wire message carries an
// unrecognised or absent schemaVersion field.
var ErrUnknownEnvelopeVersion = errcode.New(errcode.ErrEnvelopeSchema,
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
func MarshalEnvelope(entry Entry) ([]byte, error) {
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
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
		CreatedAt:     createdAt,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrEnvelopeSchema, "outbox: marshal envelope", err)
	}
	return b, nil
}

// MarshalDirectEnvelope builds a v1 wire envelope for direct-publish paths.
func MarshalDirectEnvelope(topic, eventType, id string, payload []byte) []byte {
	if id == "" {
		panic("outbox.MarshalDirectEnvelope: empty id")
	}
	if eventType == "" {
		panic("outbox.MarshalDirectEnvelope: empty eventType")
	}
	raw, err := MarshalEnvelope(Entry{
		ID:        id,
		EventType: eventType,
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		panic("outbox.MarshalDirectEnvelope: json.Marshal unexpectedly failed: " + err.Error())
	}
	return raw
}

// UnmarshalEnvelope decodes a v1 wire envelope into an Entry.
func UnmarshalEnvelope(topic string, raw []byte) (Entry, error) {
	var msg WireMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Entry{}, errcode.Wrap(errcode.ErrEnvelopeSchema,
			"outbox: unmarshal envelope", err)
	}
	if msg.SchemaVersion != EnvelopeSchemaV1 {
		return Entry{}, ErrUnknownEnvelopeVersion
	}
	if msg.ID == "" {
		return Entry{}, errcode.New(errcode.ErrEnvelopeSchema,
			"outbox: envelope missing required field: id")
	}
	if msg.EventType == "" {
		return Entry{}, errcode.New(errcode.ErrEnvelopeSchema,
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
