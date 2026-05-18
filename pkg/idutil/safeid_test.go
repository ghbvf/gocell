package idutil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeID_Validate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty is valid (zero/absent semantic)", input: "", wantErr: false},
		{name: "valid uuid-like", input: "evt-01HXY", wantErr: false},
		{name: "valid dotted event type", input: "order.created.v1", wantErr: false},
		{name: "valid with slash", input: "ns/topic-v1", wantErr: false},
		{name: "valid with colon", input: "owner:tag", wantErr: false},
		{name: "unsafe newline", input: "evt-1\nlevel=error", wantErr: true},
		{name: "unsafe CR", input: "evt-1\rINJECT", wantErr: true},
		{name: "unsafe space", input: "evt 1", wantErr: true},
		{name: "unsafe angle brackets", input: "evt<script>", wantErr: true},
		{name: "unsafe equals", input: "k=v", wantErr: true},
		{name: "unsafe plus", input: "a+b", wantErr: true},
		{name: "unsafe unicode", input: "evt-中文", wantErr: true},
		{name: "exactly max len", input: strings.Repeat("a", MaxMetadataIDLen), wantErr: false},
		{name: "over max len", input: strings.Repeat("a", MaxMetadataIDLen+1), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SafeID(tt.input).Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSafeID_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    SafeID
		wantErr bool
	}{
		{name: "valid id", json: `"evt-1"`, want: "evt-1", wantErr: false},
		{name: "empty string", json: `""`, want: "", wantErr: false},
		{name: "null becomes zero", json: `null`, want: "", wantErr: false},
		{name: "rejects newline injection", json: `"evt-1\nlevel=error"`, wantErr: true},
		{name: "rejects CR injection", json: `"evt-1\rINJECT"`, wantErr: true},
		{name: "rejects null byte", json: "\"evt\\u0000\"", wantErr: true},
		{name: "rejects space", json: `"evt 1"`, wantErr: true},
		{name: "rejects angle brackets", json: `"<script>"`, wantErr: true},
		{name: "rejects over-length", json: `"` + strings.Repeat("a", MaxMetadataIDLen+1) + `"`, wantErr: true},
		{name: "accepts max-length", json: `"` + strings.Repeat("a", MaxMetadataIDLen) + `"`, want: SafeID(strings.Repeat("a", MaxMetadataIDLen)), wantErr: false},
		{name: "rejects non-string (number)", json: `123`, wantErr: true},
		{name: "rejects malformed JSON", json: `"unterminated`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got SafeID
			err := json.Unmarshal([]byte(tt.json), &got)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSafeID_MarshalJSONRoundtrip(t *testing.T) {
	cases := []SafeID{"", "evt-1", "order.created.v1", "ns/topic:v1"}
	for _, in := range cases {
		raw, err := json.Marshal(in)
		require.NoError(t, err)
		var out SafeID
		require.NoError(t, json.Unmarshal(raw, &out))
		assert.Equal(t, in, out)
	}
}

func TestParseSafeID(t *testing.T) {
	t.Run("empty allowed", func(t *testing.T) {
		got, err := ParseSafeID("")
		require.NoError(t, err)
		assert.Equal(t, SafeID(""), got)
	})
	t.Run("valid", func(t *testing.T) {
		got, err := ParseSafeID("evt-1")
		require.NoError(t, err)
		assert.Equal(t, SafeID("evt-1"), got)
	})
	t.Run("rejects unsafe", func(t *testing.T) {
		_, err := ParseSafeID("evt 1")
		require.Error(t, err)
	})
	t.Run("rejects over-length", func(t *testing.T) {
		_, err := ParseSafeID(strings.Repeat("a", MaxMetadataIDLen+1))
		require.Error(t, err)
	})
}

