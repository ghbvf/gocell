package dto

import (
	"encoding/json"
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeEntryUpserted(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
		wantKey string
		wantVer int
	}{
		{"valid", `{"key":"k1","version":2}`, false, "k1", 2},
		{"value-field-rejected (metadata-only)", `{"key":"k1","value":"v","version":2}`, true, "", 0},
		{"missing-key", `{"version":2}`, true, "", 0},
		{"empty-key", `{"key":"","version":2}`, true, "", 0},
		{"missing-version", `{"key":"k1"}`, true, "", 0},
		{"invalid-version-zero", `{"key":"k1","version":0}`, true, "", 0},
		{"unknown-field", `{"key":"k1","version":2,"extra":1}`, true, "", 0},
		{"multiple-json-values", `{"key":"k1","version":1}{"key":"k2","version":2}`, true, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := DecodeEntryUpserted([]byte(tc.payload))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantKey, ev.Key)
			assert.Equal(t, tc.wantVer, ev.Version)
		})
	}
}

// TestDecodeEntryUpserted_AlignedWithContractSchema verifies that every
// valid payload accepted by DecodeEntryUpserted also passes the canonical
// JSON Schema at contracts/event/config/entry-upserted/v1/payload.schema.json.
// This alignment test catches decoder/schema drift early.
func TestDecodeEntryUpserted_AlignedWithContractSchema(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(), "event.config.entry-upserted.v1")

	validCases := []struct {
		name    string
		payload string
	}{
		{"minimal valid", `{"key":"k1","version":2}`},
		{"version 1", `{"key":"jwt.ttl","version":1}`},
		{"version 42", `{"key":"app.name","version":42}`},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			payloadBytes := []byte(tc.payload)

			// Decoder must accept it.
			ev, err := DecodeEntryUpserted(payloadBytes)
			require.NoError(t, err, "decoder rejected a payload that should be valid")

			// Re-encode through the decoder's output to ensure round-trip.
			encoded, err := json.Marshal(map[string]any{
				"key":     ev.Key,
				"version": ev.Version,
			})
			require.NoError(t, err)

			// Contract schema must also accept it.
			c.ValidatePayload(t, encoded)
		})
	}
}

func TestDecodeEntryDeleted(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
		wantKey string
		wantVer int
	}{
		{"valid v1", `{"key":"k1","version":1}`, false, "k1", 1},
		{"valid v42", `{"key":"k1","version":42}`, false, "k1", 42},
		{"missing-key", `{"version":1}`, true, "", 0},
		{"empty-key", `{"key":"","version":1}`, true, "", 0},
		{"missing-version", `{"key":"k1"}`, true, "", 0},
		{"version-zero", `{"key":"k1","version":0}`, true, "", 0},
		{"version-negative", `{"key":"k1","version":-1}`, true, "", 0},
		{"unknown-field", `{"key":"k1","version":1,"extra":"x"}`, true, "", 0},
		{"multiple-json-values", `{"key":"k1","version":1}{"key":"k2","version":2}`, true, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := DecodeEntryDeleted([]byte(tc.payload))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantKey, ev.Key)
			assert.Equal(t, tc.wantVer, ev.Version)
		})
	}
}

// TestDecodeEntryDeleted_AlignedWithContractSchema verifies that every
// valid payload accepted by DecodeEntryDeleted also passes the canonical
// JSON Schema at contracts/event/config/entry-deleted/v1/payload.schema.json.
func TestDecodeEntryDeleted_AlignedWithContractSchema(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(), "event.config.entry-deleted.v1")

	validCases := []struct {
		name    string
		payload string
	}{
		{"minimal valid", `{"key":"k1","version":1}`},
		{"version 42", `{"key":"app.name","version":42}`},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			payloadBytes := []byte(tc.payload)

			ev, err := DecodeEntryDeleted(payloadBytes)
			require.NoError(t, err, "decoder rejected a payload that should be valid")

			encoded, err := json.Marshal(map[string]any{
				"key":     ev.Key,
				"version": ev.Version,
			})
			require.NoError(t, err)

			c.ValidatePayload(t, encoded)
		})
	}
}
