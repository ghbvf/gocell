package outbox

import (
	"encoding/json"
	"fmt"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// EnvelopeSchemaV1 is the canonical schema version for outbox wire envelopes.
// All envelopes produced by MarshalEnvelope carry this version; UnmarshalEnvelope
// rejects any message that does not match.
const EnvelopeSchemaV1 = "v1"

// ErrUnknownEnvelopeVersion is returned by UnmarshalEnvelope when the inbound
// wire message carries an unrecognised (or absent) schemaVersion field.
// Consumers must treat this as a permanent error and route to DLX, not retry.
//
// ref: Watermill message/router.go handleMessage — unknown message type → Nack, no retry
var ErrUnknownEnvelopeVersion = errcode.New(errcode.ErrEnvelopeSchema,
	"outbox: unknown envelope schema version")

// WireMessage is the canonical wire envelope used by outbox relay publishers
// across all transports (in-memory eventbus, RabbitMQ, future Kafka). Transports
// MUST unmarshal payloads through UnmarshalEnvelope to share this contract.
//
// Fields use camelCase JSON tags, matching the serialization produced by
// adapters/postgres/outbox_relay.go publishAll and consumed by
// adapters/rabbitmq/subscriber.go unmarshalDelivery. Keeping a single canonical
// struct here replaces the duplicated outboxMessage / outboxWireMessage definitions
// scattered across those two packages.
//
// ref: Watermill message.Message — payload + metadata envelope
type WireMessage struct {
	SchemaVersion string            `json:"schemaVersion"`
	ID            string            `json:"id"`
	AggregateID   string            `json:"aggregateId,omitempty"`
	AggregateType string            `json:"aggregateType,omitempty"`
	EventType     string            `json:"eventType"`
	Topic         string            `json:"topic,omitempty"`
	Payload       json.RawMessage   `json:"payload"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	Attempts      int               `json:"attempts,omitempty"`
}

// MarshalEnvelope serializes a ClaimedEntry into the wire envelope JSON.
// The output always carries SchemaVersion = EnvelopeSchemaV1.
func MarshalEnvelope(entry ClaimedEntry) ([]byte, error) {
	msg := WireMessage{
		SchemaVersion: EnvelopeSchemaV1,
		ID:            entry.ID,
		AggregateID:   entry.AggregateID,
		AggregateType: entry.AggregateType,
		EventType:     entry.EventType,
		Topic:         entry.RoutingTopic(),
		Payload:       json.RawMessage(entry.Payload),
		Metadata:      entry.Metadata,
		CreatedAt:     entry.CreatedAt,
		Attempts:      entry.Attempts,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("outbox: marshal envelope: %w", err)
	}
	return b, nil
}

// UnmarshalEnvelope decodes a v1 wire envelope from raw bytes into a kernel/outbox.Entry.
//
// Fail-closed semantics (ref: Watermill router.go, K8s workqueue fail-closed):
//   - JSON parse failure → wrapped error (not ErrUnknownEnvelopeVersion)
//   - schemaVersion != "v1" (or absent) → ErrUnknownEnvelopeVersion
//   - empty ID or EventType → error (required fields)
//
// Legacy fallback has been removed. All producers MUST emit v1 envelopes.
// Consumers that receive non-v1 messages must Reject (route to DLX), not retry.
func UnmarshalEnvelope(topic string, raw []byte) (kout.Entry, error) {
	var msg WireMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return kout.Entry{}, fmt.Errorf("outbox: unmarshal envelope: %w", err)
	}

	if msg.SchemaVersion != EnvelopeSchemaV1 {
		return kout.Entry{}, ErrUnknownEnvelopeVersion
	}

	if msg.ID == "" {
		return kout.Entry{}, errcode.New(errcode.ErrEnvelopeSchema,
			"outbox: envelope missing required field: id")
	}
	if msg.EventType == "" {
		return kout.Entry{}, errcode.New(errcode.ErrEnvelopeSchema,
			"outbox: envelope missing required field: eventType")
	}

	return kout.Entry{
		ID:            msg.ID,
		AggregateID:   msg.AggregateID,
		AggregateType: msg.AggregateType,
		EventType:     msg.EventType,
		Topic:         msg.Topic,
		Payload:       []byte(msg.Payload),
		Metadata:      msg.Metadata,
		CreatedAt:     msg.CreatedAt,
	}, nil
}
