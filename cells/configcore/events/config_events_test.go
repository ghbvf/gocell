package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeEntryUpserted(t *testing.T) {
	tests := []struct {
		name      string
		payload   []byte
		wantValue string
	}{
		{"non-empty value", []byte(`{"key":"jwt.ttl","value":"30m","version":1}`), "30m"},
		{"empty value", []byte(`{"key":"jwt.ttl","value":"","version":1}`), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeEntryUpserted(tt.payload)
			require.NoError(t, err)
			assert.Equal(t, "jwt.ttl", got.Key)
			assert.Equal(t, tt.wantValue, got.Value)
			assert.Equal(t, 1, got.Version)
		})
	}
}

func TestDecodeEntryUpsertedRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json"), "invalid"},
		{"missing key", []byte(`{"value":"30m","version":1}`), "missing key"},
		{"blank key", []byte(`{"key":"  ","value":"30m","version":1}`), "missing key"},
		{"missing value", []byte(`{"key":"jwt.ttl","version":1}`), "missing value"},
		{"invalid version", []byte(`{"key":"jwt.ttl","value":"30m","version":0}`), "invalid version"},
		{"unknown field", []byte(`{"key":"jwt.ttl","value":"30m","version":1,"sensitive":false}`), "unknown field"},
		{"multiple values", []byte(`{"key":"jwt.ttl","value":"30m","version":1} {}`), "multiple JSON values"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeEntryUpserted(tt.payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestDecodeEntryDeleted(t *testing.T) {
	got, err := DecodeEntryDeleted([]byte(`{"key":"jwt.ttl"}`))
	require.NoError(t, err)
	assert.Equal(t, "jwt.ttl", got.Key)
}

func TestDecodeEntryDeletedRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json"), "invalid"},
		{"missing key", []byte(`{}`), "missing key"},
		{"blank key", []byte(`{"key":"  "}`), "missing key"},
		{"unknown field", []byte(`{"key":"jwt.ttl","value":"old"}`), "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeEntryDeleted(tt.payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
