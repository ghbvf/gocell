package metadata_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestMatchCellID covers the CellIDPattern regex semantics (^[a-z][a-z0-9]+$):
// lowercase ASCII letters + digits only, ≥2 chars, must start with a letter.
// Identical to AssemblyIDPattern by design — the no-dash convention enforced
// by FMT-16 / FMT-C1.
func TestMatchCellID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Valid cases — real-world cell ids in this repository.
		{"accesscore", "accesscore", true},
		{"auditcore", "auditcore", true},
		{"configcore", "configcore", true},
		{"two_char_min", "ab", true},
		{"letter_plus_digit", "a0", true},
		{"long_concat", "mdmgateway", true},
		{"trailing_digits", "core1", true},

		// Invalid cases.
		{"empty", "", false},
		{"single_char", "a", false},
		{"leading_dash_disallowed", "-foo", false},
		{"internal_dash_disallowed", "foo-bar", false},
		{"trailing_dash_disallowed", "foo-", false},
		{"uppercase_disallowed", "FooBar", false},
		{"mixed_case_disallowed", "fooBar", false},
		{"underscore_disallowed", "foo_bar", false},
		{"digit_start_disallowed", "1foo", false},
		{"whitespace_disallowed", "foo bar", false},
		{"slash_disallowed", "foo/bar", false},
		{"colon_disallowed", "foo:bar", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadata.MatchCellID(tc.in)
			if got != tc.want {
				t.Fatalf("MatchCellID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
