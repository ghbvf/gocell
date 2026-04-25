package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeEntryUpserted(t *testing.T) {
	tests := []struct {
		name        string
		payload     []byte
		wantKey     string
		wantVersion int
	}{
		{"valid metadata-only payload", []byte(`{"key":"jwt.ttl","version":1}`), "jwt.ttl", 1},
		{"version > 1", []byte(`{"key":"app.name","version":42}`), "app.name", 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeEntryUpserted(tt.payload)
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, got.Key)
			assert.Equal(t, tt.wantVersion, got.Version)
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
		{"missing key", []byte(`{"version":1}`), "missing key"},
		{"blank key", []byte(`{"key":"  ","version":1}`), "missing key"},
		{"invalid version zero", []byte(`{"key":"jwt.ttl","version":0}`), "invalid version"},
		{"invalid version negative", []byte(`{"key":"jwt.ttl","version":-1}`), "invalid version"},
		// Critical: wire carrying value field must be rejected (DisallowUnknownFields)
		{"value field present — must reject", []byte(`{"key":"jwt.ttl","value":"30m","version":1}`), "unknown field"},
		{"sensitive field present", []byte(`{"key":"jwt.ttl","version":1,"sensitive":false}`), "unknown field"},
		{"multiple values", []byte(`{"key":"jwt.ttl","version":1} {}`), "multiple JSON values"},
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
