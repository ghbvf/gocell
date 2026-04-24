// Package events defines configcore's public event wire payloads and decoders.
package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EntryUpserted is the payload for event.config.entry-upserted.v1.
type EntryUpserted struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Version int    `json:"version"`
}

// EntryDeleted is the payload for event.config.entry-deleted.v1.
type EntryDeleted struct {
	Key string `json:"key"`
}

type entryUpsertedWire struct {
	Key     string  `json:"key"`
	Value   *string `json:"value"`
	Version int     `json:"version"`
}

// DecodeEntryUpserted strictly decodes and validates event.config.entry-upserted.v1.
func DecodeEntryUpserted(data []byte) (EntryUpserted, error) {
	var wire entryUpsertedWire
	if err := decodeStrict(data, &wire); err != nil {
		return EntryUpserted{}, err
	}
	if strings.TrimSpace(wire.Key) == "" {
		return EntryUpserted{}, fmt.Errorf("entry-upserted missing key")
	}
	if wire.Value == nil {
		return EntryUpserted{}, fmt.Errorf("entry-upserted missing value for key %q", wire.Key)
	}
	if wire.Version < 1 {
		return EntryUpserted{}, fmt.Errorf("entry-upserted invalid version %d for key %q", wire.Version, wire.Key)
	}
	return EntryUpserted{Key: wire.Key, Value: *wire.Value, Version: wire.Version}, nil
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
