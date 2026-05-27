package metadata_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestMatchCellID covers the CellIDPattern regex semantics (^[a-z][a-z0-9]{1,31}$):
// lowercase ASCII letters + digits only, 2-32 chars, must start with a letter.
// Identical to AssemblyIDPattern by design — the no-dash convention enforced
// by FMT-16 / FMT-C1. 32-char upper bound feeds healthz composed-name budget
// (see pkg/scaffoldid.IdentifierPattern godoc).
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

// TestIsKnownCapability covers the CapabilityEnum predicate — only the
// three closed-set values are accepted; unknown strings and empty string
// are rejected.
func TestIsKnownCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Valid — closed enum members.
		{"postgres", "postgres", true},
		{"redis", "redis", true},
		{"rabbitmq", "rabbitmq", true},

		// Invalid — out-of-enum values.
		{"empty", "", false},
		{"foobar", "foobar", false},
		{"mysql", "mysql", false},
		{"POSTGRES_upper", "POSTGRES", false},
		{"with_space", "post gres", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadata.IsKnownCapability(tc.in)
			if got != tc.want {
				t.Fatalf("IsKnownCapability(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsValidMetadataText covers the bool predicate for free-text metadata
// fields (owner.team, owner.role, etc.) — rejects the control characters
// that would break inline YAML scalar emission or fabricate adjacent fields:
// \n (LF), \r (CR), \x00 (NUL), \t (tab). All other characters are accepted;
// full YAML safety is delegated to pkg/yamlsafe.Quote at the rendering
// boundary. Tab is included because YAML 1.2 §5.1 allows tab in plain
// scalars so yamlsafe.Quote would not add quotes, letting the tab reach the
// generated YAML (validateScaffoldID / validateScaffoldText #8 parity).
//
// Mirrors the K8s apimachinery IsDNS1123Label predicate style: a single
// bool helper exported from kernel/metadata as the syntactic constraint
// single source; callers compose their own errcode wrapping.
func TestIsValidMetadataText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Valid — common free-text shapes.
		{"empty_accepted", "", true},
		{"ascii_word", "platform", true},
		{"ascii_with_space", "platform engineering", true},
		{"ascii_with_dash", "site-reliability", true},
		{"ascii_with_colon", "team:platform", true}, // colon is YAML-safe via yamlsafe.Quote
		{"unicode_letters", "技术平台", true},
		{"emoji_ok", "team-🚀", true},
		{"punctuation_ok", "foo,bar;baz", true},

		// Invalid — control characters that break YAML scalar emission.
		{"lf_rejected", "alice\nbob", false},
		{"cr_rejected", "alice\rbob", false},
		{"crlf_rejected", "alice\r\nbob", false},
		{"nul_rejected", "alice\x00bob", false},
		{"trailing_lf_rejected", "alice\n", false},
		{"leading_lf_rejected", "\nalice", false},
		{"only_lf_rejected", "\n", false},
		// Tab rejected — YAML 1.2 §5.1 allows tab in plain scalars, so
		// yamlsafe.Quote would not quote it; must be blocked at this layer.
		{"tab_rejected", "alice\tbob", false},
		{"only_tab_rejected", "\t", false},
		{"leading_tab_rejected", "\talice", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadata.IsValidMetadataText(tc.in)
			if got != tc.want {
				t.Fatalf("IsValidMetadataText(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
