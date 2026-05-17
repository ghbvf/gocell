package scaffoldid_test

// RED tests for SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01: single-source typed
// scaffold identifier. Wave-1 RED — does not compile until kernel/scaffoldid
// exposes type ScaffoldID + Parse(raw) (ScaffoldID, error).
//
// Pattern is delegated to kernel/metadata.MatchAssemblyID (^[a-z][a-z0-9]+$);
// scaffoldid never reimplements the regex.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/scaffoldid"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestParse_Accept(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		note string
	}{
		{"foo", ""},
		{"foocell", ""},
		{"abc123", ""},
		{"a1", ""},
		{"ab", "shortest all-letter valid ID (2 chars)"},
		{"orderprocessor", ""},
		// AssemblyIDPattern (^[a-z][a-z0-9]+$) has no upper length limit by
		// design; this case verifies the current accept behavior so any future
		// upper-limit addition shows up as a test failure requiring explicit review.
		{strings.Repeat("a", 200), "no upper-length limit in AssemblyIDPattern"},
	}
	for _, tc := range cases {
		tc := tc
		name := tc.raw
		if len(name) > 20 {
			name = name[:20] + "..."
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := scaffoldid.Parse(tc.raw)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected err: %v", tc.raw, err)
			}
			if got.String() != tc.raw {
				t.Fatalf("Parse(%q) = %q, want %q", tc.raw, got.String(), tc.raw)
			}
		})
	}
}

func TestParse_Reject(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"single-letter", "a"},
		{"starts-with-digit", "9foo"},
		{"uppercase", "Foo"},
		{"dash", "foo-bar"},
		{"underscore", "foo_bar"},
		{"slash", "foo/bar"},
		{"dotdot", "..foo"},
		{"newline", "foo\nbar"},
		{"control-char", "foo\x00bar"},
		{"trailing-space", "foo "},
		{"dot", "foo.bar"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := scaffoldid.Parse(tc.in)
			if err == nil {
				t.Fatalf("Parse(%q): expected ErrValidationFailed, got nil", tc.in)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("Parse(%q): err=%v is not *errcode.Error", tc.in, err)
			}
			if ec.Code != errcode.ErrValidationFailed {
				t.Fatalf("Parse(%q): code=%q, want %q", tc.in, ec.Code, errcode.ErrValidationFailed)
			}
		})
	}
}

// Parse error MUST include the pattern in details so CLI users see what they
// should match — observability contract for CLI ergonomics.
func TestParse_ErrorIncludesPatternDetail(t *testing.T) {
	t.Parallel()
	_, err := scaffoldid.Parse("Bad")
	if err == nil {
		t.Fatal("expected error")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("err is not *errcode.Error: %v", err)
	}
	patternAttr, ok := ec.FindAttr("pattern")
	if !ok {
		t.Fatal(`Parse error missing "pattern" detail`)
	}
	got := patternAttr.Value.String()
	if !strings.Contains(got, "[a-z]") {
		t.Fatalf(`"pattern" detail = %q, want substring "[a-z]"`, got)
	}
}

// ScaffoldID has a String() method that returns the underlying string,
// allowing it to be used as a yaml/text scalar without explicit cast at
// the consumer side.
func TestScaffoldID_String(t *testing.T) {
	t.Parallel()
	id, err := scaffoldid.Parse("foocell")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := id.String(); got != "foocell" {
		t.Fatalf("String() = %q, want %q", got, "foocell")
	}
}
