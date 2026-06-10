// Package events defines configcore's internal event wire payloads and decoders.
//
// This package is internal to configcore. External cells that consume
// event.config.entry-upserted.v1 must maintain their own local decoder — the
// canonical schema is contracts/event/config/entry-upserted/v1/payload.schema.json.
//
// ref: NATS subject+bytes / Watermill payload-bytes boundary — event carries
// metadata only (key + version). Subscribers MUST refetch via
// GET /api/v1/config/{key} to obtain the current value.
//
// Internal slices of configcore may import this package directly (it's the canonical decoder for the producer cell).
package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EntryUpserted is the metadata-only payload for event.config.entry-upserted.v1.
// Subscribers MUST refetch via GET /api/v1/config/{key} to obtain the value.
type EntryUpserted struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
	ActorID string `json:"actorId"`
}

// EntryDeleted is the metadata-only payload for event.config.entry-deleted.v1.
// Version is the version of the deleted entry; subscribers use it for monotonic
// tombstone protection — a replayed older upsert must be rejected when
// event.Version <= tombstone version.
type EntryDeleted struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
	ActorID string `json:"actorId"`
}

// DecodeEntryUpserted decodes and validates event.config.entry-upserted.v1.
// Unknown fields are accepted (lenient) per ADR-202605031600 v1 schema
// evolution: producers may add optional fields without breaking consumers.
// The contract still forbids carrying the entry value in the event — that
// payload-shape rule is enforced by the schema validator during contract
// tests, not by the runtime decoder.
// ActorID is required (PR-CFG-G1 G.2): producer must populate it from the
// authenticated principal; an empty actorId indicates a contract violation.
func DecodeEntryUpserted(data []byte) (EntryUpserted, error) {
	var event EntryUpserted
	if err := decodeLenient(data, &event); err != nil {
		return EntryUpserted{}, err
	}
	if strings.TrimSpace(event.Key) == "" {
		return EntryUpserted{}, fmt.Errorf("entry-upserted missing key")
	}
	if event.Version < 1 {
		return EntryUpserted{}, fmt.Errorf("entry-upserted invalid version %d for key %q", event.Version, event.Key)
	}
	if strings.TrimSpace(event.ActorID) == "" {
		return EntryUpserted{}, fmt.Errorf("entry-upserted missing actorId for key %q", event.Key)
	}
	return event, nil
}

// DecodeEntryDeleted decodes and validates event.config.entry-deleted.v1.
// Same lenient/validation contract as DecodeEntryUpserted: unknown fields
// accepted; ActorID required.
func DecodeEntryDeleted(data []byte) (EntryDeleted, error) {
	var event EntryDeleted
	if err := decodeLenient(data, &event); err != nil {
		return EntryDeleted{}, err
	}
	if strings.TrimSpace(event.Key) == "" {
		return EntryDeleted{}, fmt.Errorf("entry-deleted missing key")
	}
	if event.Version < 1 {
		return EntryDeleted{}, fmt.Errorf("entry-deleted invalid version %d for key %q", event.Version, event.Key)
	}
	if strings.TrimSpace(event.ActorID) == "" {
		return EntryDeleted{}, fmt.Errorf("entry-deleted missing actorId for key %q", event.Key)
	}
	return event, nil
}

// decodeLenient unmarshals event payload bytes into dst. Unknown fields are
// accepted (lenient) per ADR-202605031600 v1 schema evolution: producers can
// add new optional fields without breaking existing consumers. Multiple JSON
// values in the same payload are still rejected (single-message contract).
func decodeLenient(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values in payload")
		}
		return err
	}
	return nil
}
