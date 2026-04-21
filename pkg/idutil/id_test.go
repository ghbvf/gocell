package idutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSafeID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
		desc  string
	}{
		{"abc-123", true, "letters+digits+dash"},
		{"550e8400-e29b-41d4-a716-446655440000", true, "UUID v4"},
		{"req.trace_id:v1/sub", true, "all 5 separators"},
		{"UPPER-case-Mix", true, "mixed case"},
		{"a", true, "single char"},
		{"", false, "empty"},
		{"has space", false, "space"},
		{"has\nnewline", false, "newline"},
		{`has"quote`, false, "quote"},
		{"has\x00null", false, "null byte"},
		{"has\ttab", false, "tab"},
		{"sql' OR '1'='1", false, "SQL injection"},
		{"has{braces}", false, "braces"},
		{"has<angle>", false, "angle brackets"},
		{strings.Repeat("a", 300), true, "long but valid charset"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSafeID(tt.input))
		})
	}
}

func TestIsSafeID_AllSeparators(t *testing.T) {
	for _, sep := range []string{".", "_", ":", "/", "-"} {
		t.Run(sep, func(t *testing.T) {
			assert.True(t, IsSafeID(sep))
		})
	}
}

func TestMaxHTTPIDLen(t *testing.T) {
	assert.Equal(t, 128, MaxHTTPIDLen)
}

func TestMaxMetadataIDLen(t *testing.T) {
	assert.Equal(t, 256, MaxMetadataIDLen)
}

func TestNewUUID_Format(t *testing.T) {
	id := NewUUID()
	assert.Len(t, id, 36, "UUID must be 36 chars: 8-4-4-4-12")

	// Dash positions: 8, 13, 18, 23
	assert.Equal(t, byte('-'), id[8])
	assert.Equal(t, byte('-'), id[13])
	assert.Equal(t, byte('-'), id[18])
	assert.Equal(t, byte('-'), id[23])

	// Version 4 indicator at position 14
	assert.Equal(t, byte('4'), id[14])

	// Variant bits at position 19: must be 8, 9, a, or b
	assert.Contains(t, "89ab", string(id[19]))
}

func TestNewUUID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := NewUUID()
		_, dup := seen[id]
		require.False(t, dup, "duplicate UUID: %s", id)
		seen[id] = struct{}{}
	}
}

func TestNewUUID_PassesIsSafeID(t *testing.T) {
	for range 100 {
		id := NewUUID()
		assert.True(t, IsSafeID(id), "generated UUID %q must pass IsSafeID", id)
		assert.LessOrEqual(t, len(id), MaxHTTPIDLen)
	}
}
