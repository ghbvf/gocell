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

// TestDecodeEntryUpserted_AcceptsExtraFields locks down ADR-202605031600 v1
// schema evolution: producers may add optional fields, and consumers must
// not reject them. The schema validator (contracts/.../payload.schema.json)
// is the source of truth for "value field forbidden in event"; the runtime
// decoder is intentionally lenient.
func TestDecodeEntryUpserted_AcceptsExtraFields(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"extra value field", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1","value":"30m"}`)},
		{"extra sensitive field", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1","sensitive":false}`)},
		{"future producer field", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1","traceId":"abc-123"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeEntryUpserted(tc.payload)
			require.NoError(t, err, "lenient decoder must accept extra fields per ADR-202605031600")
			assert.Equal(t, "jwt.ttl", got.Key)
			assert.Equal(t, 1, got.Version)
			assert.Equal(t, "admin-1", got.ActorID)
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
