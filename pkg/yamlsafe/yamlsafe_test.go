package yamlsafe_test

import (
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
