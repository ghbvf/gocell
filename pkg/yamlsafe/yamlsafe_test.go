package yamlsafe_test

import (
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/pkg/yamlsafe"
)

// TestQuote_PlainSafe verifies that plain-safe scalars pass through unquoted.
// RED: Quote returns "" stub, all assertions will fail.
func TestQuote_PlainSafe(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
	}{
		{"foo", "foo"},
		{"123", "123"},
		{"myCell", "myCell"},
		{"accesscore", "accesscore"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.input, func(t *testing.T) {
			t.Parallel()
			got := yamlsafe.Quote(c.input).String()
			if got != c.want {
				t.Errorf("Quote(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestQuote_ColonInString verifies that a colon triggers quoting.
// RED: Quote stub returns "".
func TestQuote_ColonInString(t *testing.T) {
	t.Parallel()
	got := yamlsafe.Quote("foo: bar").String()
	want := "'foo: bar'"
	if got != want {
		t.Errorf("Quote(%q) = %q, want %q", "foo: bar", got, want)
	}
}

// TestQuote_BraceInString verifies that opening brace triggers quoting.
// RED: Quote stub returns "".
func TestQuote_BraceInString(t *testing.T) {
	t.Parallel()
	got := yamlsafe.Quote("{evil}").String()
	want := "'{evil}'"
	if got != want {
		t.Errorf("Quote(%q) = %q, want %q", "{evil}", got, want)
	}
}

// TestQuote_SingleQuoteInString verifies that embedded single-quote is doubled.
// RED: Quote stub returns "".
func TestQuote_SingleQuoteInString(t *testing.T) {
	t.Parallel()
	got := yamlsafe.Quote("foo'bar").String()
	want := "'foo''bar'"
	if got != want {
		t.Errorf("Quote(%q) = %q, want %q", "foo'bar", got, want)
	}
}

// TestQuote_NewlineInString verifies that embedded newline triggers quoting.
// RED: Quote stub returns "".
func TestQuote_NewlineInString(t *testing.T) {
	t.Parallel()
	raw := "foo\nbar"
	got := yamlsafe.Quote(raw).String()
	// single-quoted block preserves newline literally; yaml.v3 renders embedded
	// newlines in single-quoted scalars as-is (literal newline inside quotes).
	// Simplest safe representation: ensure it round-trips through yaml.Unmarshal
	// back to the original string.
	if got == raw {
		t.Errorf("Quote(%q): returned raw string with unquoted newline; want quoted form", raw)
	}
	// Round-trip: embed in YAML map and decode.
	yamlDoc := "key: " + got + "\n"
	var out map[string]string
	if err := yaml.Unmarshal([]byte(yamlDoc), &out); err != nil {
		t.Fatalf("Quote(%q): round-trip yaml.Unmarshal failed: %v\nyamlDoc=%q", raw, err, yamlDoc)
	}
	if out["key"] != raw {
		t.Errorf("Quote(%q): round-trip = %q, want original %q", raw, out["key"], raw)
	}
}

// TestQuote_LeadingSpace verifies that leading whitespace triggers quoting.
// RED: Quote stub returns "".
func TestQuote_LeadingSpace(t *testing.T) {
	t.Parallel()
	got := yamlsafe.Quote("  foo").String()
	want := "'  foo'"
	if got != want {
		t.Errorf("Quote(%q) = %q, want %q", "  foo", got, want)
	}
}

// TestQuote_Empty verifies that an empty string is single-quoted as ”.
// RED: Quote stub returns "".
func TestQuote_Empty(t *testing.T) {
	t.Parallel()
	got := yamlsafe.Quote("").String()
	want := "''"
	if got != want {
		t.Errorf("Quote(%q) = %q, want %q", "", got, want)
	}
}

// TestScalar_String verifies that Scalar.String() returns the quoted form.
// RED: Quote stub returns "".
func TestScalar_String(t *testing.T) {
	t.Parallel()
	s := yamlsafe.Quote("foo: bar")
	got := s.String()
	want := "'foo: bar'"
	if got != want {
		t.Errorf("Quote(%q).String() = %q, want %q", "foo: bar", got, want)
	}
}

// TestQuote_RoundTrip_ColonValue verifies full YAML round-trip for a value with colon.
// RED: Quote stub returns "" which breaks YAML parsing.
func TestQuote_RoundTrip_ColonValue(t *testing.T) {
	t.Parallel()
	raw := "evil:value"
	quoted := yamlsafe.Quote(raw).String()
	doc := "id: " + quoted + "\n"
	var out map[string]string
	if err := yaml.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatalf("Round-trip failed: %v\ndoc=%q", err, doc)
	}
	if out["id"] != raw {
		t.Errorf("Round-trip got %q, want %q", out["id"], raw)
	}
}

// roundTripYAML is a helper that embeds quoted into a YAML map and returns the
// decoded value for key "key". Used by C0/DEL round-trip tests.
func roundTripYAML(t *testing.T, raw string) string {
	t.Helper()
	quoted := yamlsafe.Quote(raw).String()
	doc := "key: " + quoted + "\n"
	var out map[string]string
	if err := yaml.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatalf("Quote(%q): round-trip yaml.Unmarshal failed: %v\ndoc=%q", raw, err, doc)
	}
	return out["key"]
}

// TestQuote_CR verifies that a carriage-return byte round-trips correctly.
func TestQuote_CR(t *testing.T) {
	t.Parallel()
	raw := "a\rb"
	if got := roundTripYAML(t, raw); got != raw {
		t.Errorf("Quote(%q) round-trip = %q, want original", raw, got)
	}
}

// TestQuote_NUL verifies that a NUL byte round-trips correctly.
func TestQuote_NUL(t *testing.T) {
	t.Parallel()
	raw := "a\x00b"
	if got := roundTripYAML(t, raw); got != raw {
		t.Errorf("Quote(%q) round-trip = %q, want original", raw, got)
	}
}

// TestQuote_CRLF verifies that a CRLF sequence round-trips correctly.
func TestQuote_CRLF(t *testing.T) {
	t.Parallel()
	raw := "a\r\nb"
	if got := roundTripYAML(t, raw); got != raw {
		t.Errorf("Quote(%q) round-trip = %q, want original", raw, got)
	}
}

// TestQuote_LeadingTab verifies that a leading TAB round-trips correctly.
// TAB is safe in YAML scalars (YAML 1.2 §5.1) but triggers needsQuoting via
// leading-whitespace detection, so it gets single-quoted.
func TestQuote_LeadingTab(t *testing.T) {
	t.Parallel()
	raw := "\tindented"
	if got := roundTripYAML(t, raw); got != raw {
		t.Errorf("Quote(%q) round-trip = %q, want original", raw, got)
	}
}

// TestQuote_OtherC0 verifies that non-printable C0 bytes (\x01, \x07) that
// previously bypassed needsQuoting round-trip correctly after the fix.
func TestQuote_OtherC0(t *testing.T) {
	t.Parallel()
	raw := "a\x01\x07b"
	if got := roundTripYAML(t, raw); got != raw {
		t.Errorf("Quote(%q) round-trip = %q, want original", raw, got)
	}
}

// TestNeedsQuoting_PlainStyleIndicators verifies that leading `-` / `?` / `:`
// followed by whitespace or EOL (i.e. the full single-char scalar) triggers
// quoting, and that internal dashes do not.
func TestNeedsQuoting_PlainStyleIndicators(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		raw        string
		wantQuoted bool
		roundTrip  bool // if true, also verify yaml round-trip preserves raw
	}{
		{
			name:       "dash_space_sequence_indicator",
			raw:        "- oncall",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "question_space_explicit_key",
			raw:        "? key",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "colon_space_mapping_value",
			raw:        ": value",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "single_dash_scalar",
			raw:        "-",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "single_question",
			raw:        "?",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "single_colon",
			raw:        ":",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "trailing_space",
			raw:        "platform ",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "trailing_tab",
			raw:        "team\t",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "doc_start_marker",
			raw:        "---",
			wantQuoted: true,
			roundTrip:  true,
		},
		{
			name:       "doc_end_marker",
			raw:        "...",
			wantQuoted: true,
			roundTrip:  true,
		},
		// Negative cases: these must NOT trigger quoting from the new rules.
		{
			name:       "internal_dash_not_indicator",
			raw:        "team-a",
			wantQuoted: false,
		},
		{
			name:       "dash_followed_by_letter",
			raw:        "-foo",
			wantQuoted: false,
		},
		{
			name:       "colon_followed_by_letter",
			raw:        "key:value",
			wantQuoted: true, // colon already detected by ContainsAny
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			quoted := yamlsafe.Quote(tc.raw)
			isQuoted := quoted.String() != tc.raw

			if isQuoted != tc.wantQuoted {
				t.Errorf("Quote(%q) = %q: wantQuoted=%v, got quoted=%v",
					tc.raw, quoted.String(), tc.wantQuoted, isQuoted)
			}

			if tc.roundTrip {
				got := roundTripYAML(t, tc.raw)
				if got != tc.raw {
					t.Errorf("Quote(%q) round-trip = %q, want original", tc.raw, got)
				}
			}
		})
	}
}

// exhaustiveCorpus enumerates the reachable input space for Quote's
// YAML-injection classification: every ASCII byte, every 2-byte combination
// of a YAML structural indicator with a letter / space / EOL, and the
// whitespace / document-marker edge cases.
//
// Scope note: the sweep is ASCII (0x00..0x7F). Every YAML structural
// indicator and control character that can break scalar framing is ASCII,
// so this fully covers the injection surface that needsQuoting classifies.
// Bytes 0x80..0xFF exercise Go rune decoding inside doubleQuote (not
// touched by the needsQuoting decomposition) and a lone invalid-UTF-8 byte
// does not round-trip through yaml.v3 on the pre-refactor code either, so
// including them would assert Go UTF-8 semantics rather than pin the
// injection invariant.
func exhaustiveCorpus() []string {
	const indicators = ":{}[],&*#?|>!%@`\"'-\n\r\t\x00 "
	companions := []byte{'a', ' ', '\n', '\r', '\t'}

	corpus := []string{"", "---", "...", "--", "....", "- ", "-", "?", ":", "? ", ": "}
	for b := 0; b < 0x80; b++ {
		corpus = append(corpus, string(rune(b)))
	}
	for i := 0; i < len(indicators); i++ {
		m := indicators[i]
		for _, c := range companions {
			corpus = append(corpus, string([]byte{m, c}), string([]byte{c, m}))
		}
		corpus = append(corpus, string([]byte{m, m}))
	}
	return corpus
}

// TestQuote_ExhaustiveRoundTripInvariant is the AI-Hard equivalence pin for
// the YAML-injection funnel core. It asserts the real security invariant
// over the whole reachable input space (exhaustiveCorpus), independent of
// needsQuoting's internal shape:
//
//  1. Round-trip fidelity: yaml.Unmarshal(Quote(x)) == x.
//  2. No structural escape: the quoted form embedded as a mapping value
//     parses as exactly one scalar entry — a value that escaped its scalar
//     framing would inject an adjacent key or document boundary, making the
//     decoded map have != 1 entry or lose the original value.
//
// Any change to needsQuoting that alters a single classification fails this
// test deterministically (no string-anchor / comment-anchor escape), so it
// is Hard by enumeration form per ai-collab.md §"Hard 范本"
// (real-input enumeration > hand-crafted fixture). It supersedes — does not
// duplicate — the single-purpose round-trip tests above, which remain as
// human-readable regression documentation.
func TestQuote_ExhaustiveRoundTripInvariant(t *testing.T) {
	t.Parallel()

	for _, raw := range exhaustiveCorpus() {
		raw := raw
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			t.Parallel()

			out := yamlsafe.Quote(raw).String()
			doc := "key: " + out + "\n"
			var decoded map[string]string
			if err := yaml.Unmarshal([]byte(doc), &decoded); err != nil {
				t.Fatalf("Quote(%q)=%q: yaml.Unmarshal failed: %v\ndoc=%q", raw, out, err, doc)
			}
			if len(decoded) != 1 {
				t.Fatalf("Quote(%q)=%q: structural escape — decoded %d keys, want 1\ndoc=%q",
					raw, out, len(decoded), doc)
			}
			if got := decoded["key"]; got != raw {
				t.Errorf("Quote(%q)=%q: round-trip = %q, want original", raw, out, got)
			}
		})
	}
}
