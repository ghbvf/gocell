package outbox

import (
	"encoding/json"
	"fmt"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/google/uuid"
)

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
func MarshalEnvelope(entry ClaimedEntry) ([]byte, error) {
	msg := WireMessage{
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

// UnmarshalEnvelope tries to decode raw as a WireMessage envelope. If raw
// looks like an envelope (has "id" + "eventType" + "payload" fields and
// Payload starts with '{' or '['), it extracts payload + metadata into
// a kernel/outbox.Entry. Otherwise it wraps raw as a fresh Entry with
// topic as eventType (fallback compatibility with non-outbox publishes).
//
// This function replaces duplicated logic in runtime/eventbus/eventbus.go
// (unmarshalInboundEntry) and adapters/rabbitmq/subscriber.go (unmarshalDelivery).
//
// Discriminator: require non-empty ID + EventType AND an embedded JSON payload
// (starts with '{' or '['), matching the check in both transport adapters.
// Business payloads that happen to parse as an envelope structure are guarded
// by the isEmbeddedJSON check (very unlikely to be mis-detected).
//
// Fallback: raw is wrapped in a freshly stamped Entry with ID = "evt-" + UUID,
// EventType = topic, and CreatedAt = time.Now(). This preserves pre-envelope
// direct-publish semantics (InMemoryEventBus path).
func UnmarshalEnvelope(topic string, raw []byte) (kout.Entry, error) {
	var msg WireMessage
	if err := json.Unmarshal(raw, &msg); err == nil &&
		msg.ID != "" && msg.EventType != "" && isEmbeddedJSON(msg.Payload) {
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
	// Fallback: wrap as a fresh entry (direct-publish / non-envelope semantics).
	// Mirror of runtime/eventbus/eventbus.go unmarshalInboundEntry fallback branch.
	return kout.Entry{
		ID:        "evt-" + uuid.NewString(),
		EventType: topic,
		Payload:   raw,
		CreatedAt: time.Now(),
	}, nil
}

// isEmbeddedJSON returns true if the raw JSON value is an object or array
// (relay envelope payload), not a base64 string or primitive.
// Mirrors the identical helpers in runtime/eventbus and adapters/rabbitmq.
func isEmbeddedJSON(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}
