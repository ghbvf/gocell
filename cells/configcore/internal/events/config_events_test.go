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
		{"valid metadata-only payload", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1"}`), "jwt.ttl", 1},
		{"version > 1", []byte(`{"key":"app.name","version":42,"actorId":"admin-1"}`), "app.name", 42},
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
		{"missing key", []byte(`{"version":1,"actorId":"admin-1"}`), "missing key"},
		{"blank key", []byte(`{"key":"  ","version":1,"actorId":"admin-1"}`), "missing key"},
		{"invalid version zero", []byte(`{"key":"jwt.ttl","version":0,"actorId":"admin-1"}`), "invalid version"},
		{"invalid version negative", []byte(`{"key":"jwt.ttl","version":-1,"actorId":"admin-1"}`), "invalid version"},
		{"missing actorId", []byte(`{"key":"jwt.ttl","version":1}`), "missing actorId"},
		{"empty actorId", []byte(`{"key":"jwt.ttl","version":1,"actorId":""}`), "missing actorId"},
		{"blank actorId", []byte(`{"key":"jwt.ttl","version":1,"actorId":"   "}`), "missing actorId"},
		// Critical: wire carrying value field must be rejected (DisallowUnknownFields)
		{"value field present — must reject", []byte(`{"key":"jwt.ttl","value":"30m","version":1,"actorId":"admin-1"}`), "unknown field"},
		{"sensitive field present", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1","sensitive":false}`), "unknown field"},
		{"multiple values", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1"} {}`), "multiple JSON values"},
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
	tests := []struct {
		name        string
		payload     []byte
		wantErr     bool
		wantKey     string
		wantVersion int
		errContains string
	}{
		{"valid v1", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1"}`), false, "jwt.ttl", 1, ""},
		{"valid v42", []byte(`{"key":"app.name","version":42,"actorId":"admin-1"}`), false, "app.name", 42, ""},
		{"missing key", []byte(`{"version":1,"actorId":"admin-1"}`), true, "", 0, "missing key"},
		{"blank key", []byte(`{"key":"  ","version":1,"actorId":"admin-1"}`), true, "", 0, "missing key"},
		{"missing version", []byte(`{"key":"jwt.ttl","actorId":"admin-1"}`), true, "", 0, "invalid version"},
		{"version zero", []byte(`{"key":"jwt.ttl","version":0,"actorId":"admin-1"}`), true, "", 0, "invalid version"},
		{"version negative", []byte(`{"key":"jwt.ttl","version":-1,"actorId":"admin-1"}`), true, "", 0, "invalid version"},
		{"missing actorId", []byte(`{"key":"jwt.ttl","version":1}`), true, "", 0, "missing actorId"},
		{"empty actorId", []byte(`{"key":"jwt.ttl","version":1,"actorId":""}`), true, "", 0, "missing actorId"},
		{"unknown field", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1","value":"old"}`), true, "", 0, "unknown field"},
		{"multiple json values", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1"}{}`), true, "", 0, "multiple JSON values"},
		{"invalid json", []byte("not-json"), true, "", 0, "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeEntryDeleted(tt.payload)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, got.Key)
			assert.Equal(t, tt.wantVersion, got.Version)
		})
	}
}
