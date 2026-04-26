// Package dto contains accesscore's local typed views of cross-cell event payloads.
//
// Per cell-patterns.md, contracts/ JSON Schema is the single source of truth for
// inter-cell wire format; each consuming cell maintains its own typed view +
// decoder rather than importing the producer cell's Go types. The ~40 LoC of
// duplicated decode logic between accesscore/configreceive and configcore is
// the accepted cost of cell isolation.
//
// ref: NATS subject+bytes / Watermill payload-bytes / go-micro broker boundary.
package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EntryUpserted is accesscore's local typed view of event.config.entry-upserted.v1.
// Schema source of truth: contracts/event/config/entry-upserted/v1/payload.schema.json.
// Metadata-only: payload carries key + version; subscribers refetch via
// GET /api/v1/config/{key} when they actually need the value.
type EntryUpserted struct {
	Key     string
	Version int
	ActorID string
}

// EntryDeleted is accesscore's local typed view of event.config.entry-deleted.v1.
// Version is the version of the deleted entry; consumers use it for monotonic
// tombstone protection — a replayed older upsert must be rejected when
// event.Version <= tombstone version.
type EntryDeleted struct {
	Key     string
	Version int
	ActorID string
}

// internal wire structs; not exported
type entryUpsertedWire struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
	ActorID string `json:"actorId"`
}

type entryDeletedWire struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
	ActorID string `json:"actorId"`
}

// DecodeEntryUpserted strictly decodes and validates event.config.entry-upserted.v1.
// Rejects unknown fields (including legacy "value") and enforces non-empty key + version >= 1.
func DecodeEntryUpserted(data []byte) (EntryUpserted, error) {
	var wire entryUpsertedWire
	if err := decodeStrict(data, &wire); err != nil {
		return EntryUpserted{}, err
	}
	if strings.TrimSpace(wire.Key) == "" {
		return EntryUpserted{}, fmt.Errorf("entry-upserted missing key")
	}
	if wire.Version < 1 {
		return EntryUpserted{}, fmt.Errorf("entry-upserted invalid version %d for key %q", wire.Version, wire.Key)
	}
	return EntryUpserted(wire), nil
}

// DecodeEntryDeleted strictly decodes and validates event.config.entry-deleted.v1.
// Rejects unknown fields and enforces non-empty key and version >= 1.
func DecodeEntryDeleted(data []byte) (EntryDeleted, error) {
	var wire entryDeletedWire
	if err := decodeStrict(data, &wire); err != nil {
		return EntryDeleted{}, err
	}
	if strings.TrimSpace(wire.Key) == "" {
		return EntryDeleted{}, fmt.Errorf("entry-deleted missing key")
	}
	if wire.Version < 1 {
		return EntryDeleted{}, fmt.Errorf("entry-deleted invalid version %d for key %q", wire.Version, wire.Key)
	}
	return EntryDeleted(wire), nil
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
