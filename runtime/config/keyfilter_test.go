package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyFilter_Matches(t *testing.T) {
	tests := []struct {
		name     string
		prefixes []string
		keys     []string
		want     bool
	}{
		{
			name:     "single prefix match",
			prefixes: []string{"server."},
			keys:     []string{"server.port"},
			want:     true,
		},
		{
			name:     "single prefix no match",
			prefixes: []string{"server."},
			keys:     []string{"db.host"},
			want:     false,
		},
		{
			name:     "multiple prefixes one match",
			prefixes: []string{"server.", "db."},
			keys:     []string{"db.host"},
			want:     true,
		},
		{
			name:     "multiple keys one match",
			prefixes: []string{"server."},
			keys:     []string{"db.host", "server.port"},
			want:     true,
		},
		{
			name:     "empty filter matches everything",
			prefixes: nil,
			keys:     []string{"anything"},
			want:     true,
		},
		{
			name:     "empty filter empty keys",
			prefixes: nil,
			keys:     nil,
			want:     true,
		},
		{
			name:     "non-empty filter empty keys",
			prefixes: []string{"server."},
			keys:     nil,
			want:     false,
		},
		{
			name:     "exact prefix match",
			prefixes: []string{"server.port"},
			keys:     []string{"server.port"},
			want:     true,
		},
		{
			name:     "prefix is substring of key",
			prefixes: []string{"server"},
			keys:     []string{"server.port"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewKeyFilter(tt.prefixes...)
			assert.Equal(t, tt.want, f.Matches(tt.keys))
		})
	}
}
