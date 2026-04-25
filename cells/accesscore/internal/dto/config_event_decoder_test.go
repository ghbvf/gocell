package dto

import (
	"testing"

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

func TestDecodeEntryDeleted(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
		wantKey string
	}{
		{"valid", `{"key":"k1"}`, false, "k1"},
		{"missing-key", `{}`, true, ""},
		{"empty-key", `{"key":""}`, true, ""},
		{"unknown-field", `{"key":"k1","extra":"x"}`, true, ""},
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
		})
	}
}
