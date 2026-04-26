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

// DecodeEntryUpserted strictly decodes and validates event.config.entry-upserted.v1.
// The payload must not contain a "value" field — this decoder rejects unknown fields.
func DecodeEntryUpserted(data []byte) (EntryUpserted, error) {
	var event EntryUpserted
	if err := decodeStrict(data, &event); err != nil {
		return EntryUpserted{}, err
	}
	if strings.TrimSpace(event.Key) == "" {
		return EntryUpserted{}, fmt.Errorf("entry-upserted missing key")
	}
	if event.Version < 1 {
		return EntryUpserted{}, fmt.Errorf("entry-upserted invalid version %d for key %q", event.Version, event.Key)
	}
	return event, nil
}

// DecodeEntryDeleted strictly decodes and validates event.config.entry-deleted.v1.
func DecodeEntryDeleted(data []byte) (EntryDeleted, error) {
	var event EntryDeleted
	if err := decodeStrict(data, &event); err != nil {
		return EntryDeleted{}, err
	}
	if strings.TrimSpace(event.Key) == "" {
		return EntryDeleted{}, fmt.Errorf("entry-deleted missing key")
	}
	if event.Version < 1 {
		return EntryDeleted{}, fmt.Errorf("entry-deleted invalid version %d for key %q", event.Version, event.Key)
	}
	return event, nil
}

func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
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
