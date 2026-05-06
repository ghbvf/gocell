package metadata_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
)

func TestNewGoIdentifier(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is zero", "", false},
		{"capitalised CamelCase", "AccessCore", false},
		{"single letter", "A", false},
		{"with digits", "OAuth2", false},
		{"lowercase start", "lowercase", true},
		{"underscore start", "_Foo", true},
		{"digit start", "123Foo", true},
		{"contains dash", "Foo-bar", true},
		{"contains space", "Foo Bar", true},
		{"semicolon injection", "Foo;package os;func init(){}//", true},
		{"newline injection", "Foo\n}package main", true},
		{"non-ASCII", "日本", true},
		{"sigma", "σ", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := metadata.NewGoIdentifier(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.in == "" {
				assert.True(t, got.IsZero())
			} else {
				assert.False(t, got.IsZero())
				assert.Equal(t, tc.in, got.String())
			}
		})
	}
}

// TestGoIdentifier_UnmarshalYAML_RejectsInjection asserts that yaml.Unmarshal
// dispatches to GoIdentifier.UnmarshalYAML and rejects malicious input at
// parse time; downstream codegen never sees an invalid value.
func TestGoIdentifier_UnmarshalYAML_RejectsInjection(t *testing.T) {
	t.Parallel()
	cases := []string{
		"Foo;package os;func init(){os.Exit(0)}//",
		"Foo\n}package main",
		"lowercase",
		"my-cell",
	}
	for _, raw := range cases {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			var g metadata.GoIdentifier
			err := yaml.Unmarshal([]byte(raw), &g)
			require.Error(t, err, "expected unmarshal to reject %q", raw)
		})
	}
}

// TestGoIdentifier_UnmarshalYAML_AcceptsValid covers the happy path so the
// hook itself is exercised under round-trip.
func TestGoIdentifier_UnmarshalYAML_AcceptsValid(t *testing.T) {
	t.Parallel()
	var g metadata.GoIdentifier
	require.NoError(t, yaml.Unmarshal([]byte("AccessCore\n"), &g))
	assert.Equal(t, "AccessCore", g.String())

	out, err := yaml.Marshal(g)
	require.NoError(t, err)
	assert.Equal(t, "AccessCore\n", string(out))
}

// TestGoIdentifier_UnmarshalYAML_RejectsNonString covers the "yaml node type
// mismatch" branch — passing an integer where a string is expected must be
// reported by node.Decode rather than reaching NewGoIdentifier.
func TestGoIdentifier_UnmarshalYAML_RejectsNonString(t *testing.T) {
	t.Parallel()
	var g metadata.GoIdentifier
	require.Error(t, yaml.Unmarshal([]byte("42\n"), &g))
}
