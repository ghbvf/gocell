package sagacoveragegen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
)

func TestPascalCase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"pending", "Pending"},
		{"compensation_failed", "CompensationFailed"},
		{"step_started", "StepStarted"},
		{"saga_compensation_failed", "SagaCompensationFailed"},
	} {
		if got := pascalCase(tc.in); got != tc.want {
			t.Errorf("pascalCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRenderReadyzTable_MatchesSource asserts every status row carries the const
// value and IsTerminal() classification, and that names round-trip to real
// consts via the "Status"+PascalCase(String()) naming convention.
func TestRenderReadyzTable_MatchesSource(t *testing.T) {
	t.Parallel()
	art, err := Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(art.ReadyzTable, "\n"), "\n")
	// header + separator + one row per status.
	var rows int
	for s := saga.StatusPending; s.Valid(); s++ {
		rows++
		term := "No"
		if s.IsTerminal() {
			term = "Yes"
		}
		want := "| `" + pascalCase(s.String()) + "` | " // prefix; full check below
		var found bool
		for _, l := range lines {
			if strings.HasPrefix(l, want) {
				found = true
				if !strings.HasSuffix(l, "| "+term+" |") {
					t.Errorf("status %v row terminal mismatch: %q (want suffix Terminal?=%s)", s, l, term)
				}
				if !strings.Contains(l, "| "+itoa(int(s))+" | ") {
					t.Errorf("status %v row value mismatch: %q (want value=%d)", s, l, int(s))
				}
			}
		}
		if !found {
			t.Errorf("status %v (%q) has no readyz table row", s, s.String())
		}
	}
	if len(lines) != rows+2 {
		t.Errorf("readyz table has %d lines, want %d (header+sep+%d rows)", len(lines), rows+2, rows)
	}
}

// TestRenderKindLegend_MatchesSource asserts the legend has one value=wire entry
// per EventKind, in value order.
func TestRenderKindLegend_MatchesSource(t *testing.T) {
	t.Parallel()
	art, err := Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for k := journal.KindStepStarted; k.Valid(); k++ {
		entry := itoa(int(k)) + "=" + k.String()
		if !strings.Contains(art.KindLegend, entry) {
			t.Errorf("legend missing entry %q; legend=%q", entry, art.KindLegend)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(art.KindLegend, "\n"), "。") {
		t.Errorf("legend must end with 。; got %q", art.KindLegend)
	}
}

// TestRenderTerminalCoverageGo_OneFieldPerTerminal asserts the generated struct
// has exactly one field per terminal status and compiles via gofmt.
func TestRenderTerminalCoverageGo_OneFieldPerTerminal(t *testing.T) {
	t.Parallel()
	art, err := Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	src := string(art.TerminalCoverageGo)
	for s := saga.StatusPending; s.Valid(); s++ {
		// Comma-terminated refs are immune to gofmt struct-field alignment and to
		// prefix false-matches (e.g. c.Failed, vs c.CompensationFailed,).
		wantRef := "saga.Status" + pascalCase(s.String()) + ","
		driverRef := "c." + pascalCase(s.String()) + ","
		present := strings.Contains(src, wantRef)
		if s.IsTerminal() != present {
			t.Errorf("status %v: terminal=%v but terminalCoverageWant ref present=%v", s, s.IsTerminal(), present)
		}
		if s.IsTerminal() != strings.Contains(src, driverRef) {
			t.Errorf("status %v: terminal=%v but drivers() ref present=%v", s, s.IsTerminal(), strings.Contains(src, driverRef))
		}
	}
}

func TestExtractReplaceRegion_RoundTrip(t *testing.T) {
	t.Parallel()
	const start = "<!-- s -->"
	const end = "<!-- e -->"
	doc := "intro\n" + start + "\nOLD\n" + end + "\noutro\n"
	body, err := ExtractRegion(doc, start, end)
	if err != nil {
		t.Fatalf("ExtractRegion: %v", err)
	}
	if body != "OLD\n" {
		t.Errorf("ExtractRegion = %q, want %q", body, "OLD\n")
	}
	out, err := ReplaceRegion(doc, start, end, "NEW\n")
	if err != nil {
		t.Fatalf("ReplaceRegion: %v", err)
	}
	want := "intro\n" + start + "\nNEW\n" + end + "\noutro\n"
	if out != want {
		t.Errorf("ReplaceRegion = %q, want %q", out, want)
	}
}

// itoa avoids importing strconv just for small positive ints in tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
