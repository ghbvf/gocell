package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const (
	// testThreshold is the budget used in evaluate / renderViolations
	// fixtures. Decoupled from the binary's defaultThreshold — these
	// tests exercise threshold-comparison behavior at an arbitrary
	// cutoff, not the production default. The fixed `2 * time.Second`
	// value is chosen so output-format assertions can match the literal
	// "2s" string emitted by `time.Duration.String()`; changes to
	// defaultThreshold do not affect these fixtures.
	testThreshold = 2 * time.Second
	// nilAllowlistOverElapsed is a synthetic over-budget elapsed used by
	// TestEvaluate_NilAllowlistMeansNoneAllowed.
	nilAllowlistOverElapsed = 3 * time.Second
	// renderViolationFastElapsed and renderViolationSlowElapsed are the two
	// distinct magnitudes used to assert deterministic sort order in
	// TestRenderViolations_Format.
	renderViolationFastElapsed = 2100 * time.Millisecond
	renderViolationSlowElapsed = 3500 * time.Millisecond
)

func TestParseAllowlist(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   string
		want    []string // expected keys (Package\tTest)
		wantErr string
	}{
		{
			name:  "tab_separated",
			input: "github.com/x/y\tTestFoo\n",
			want:  []string{"github.com/x/y\tTestFoo"},
		},
		{
			name:  "space_separated_fallback",
			input: "github.com/x/y TestFoo\n",
			want:  []string{"github.com/x/y\tTestFoo"},
		},
		{
			name:  "multi_space_collapses",
			input: "github.com/x/y    TestFoo\n",
			want:  []string{"github.com/x/y\tTestFoo"},
		},
		{
			name: "comments_and_blanks_ignored",
			input: "" +
				"# header comment\n" +
				"\n" +
				"github.com/a TestA\n" +
				"   # indented comment\n" +
				"github.com/b\tTestB\n",
			want: []string{"github.com/a\tTestA", "github.com/b\tTestB"},
		},
		{
			name:  "inline_comment_after_data_rejected",
			input: "github.com/a TestA # not a comment in this slot\n",
			// `#` is only meaningful at the start of a line (after optional
			// whitespace). On a data line, anything after the test name is
			// extra content. The whitespace-fallback path passes this line
			// through `strings.Fields` which produces 6 tokens; the
			// resulting >2 field count is what triggers the "extra fields"
			// rejection — NOT any special handling of `#`. A line like
			// `github.com/a TestA#suffix` (no space before `#`) would parse
			// as a 2-field line with TestName="TestA#suffix"; this is
			// surfaced by SLOWGATE-ALLOWLIST-01 (no such test exists).
			wantErr: "extra fields",
		},
		{
			name:    "single_field_rejected",
			input:   "github.com/a\n",
			wantErr: "expected 2 fields",
		},
		{
			name:    "empty_test_name_rejected",
			input:   "github.com/a\t\n",
			wantErr: "empty test name",
		},
		{
			name:    "extra_fields_rejected",
			input:   "github.com/a\tTestA\textra\n",
			wantErr: "extra fields",
		},
		{
			name:  "carriage_return_stripped",
			input: "github.com/a TestA\r\n",
			want:  []string{"github.com/a\tTestA"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseAllowlist(strings.NewReader(tc.input))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseAllowlist: want err containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseAllowlist: want err containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAllowlist: unexpected err: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseAllowlist: want %d entries, got %d (%v)", len(tc.want), len(got), got)
			}
			for _, key := range tc.want {
				if _, ok := got[key]; !ok {
					t.Errorf("parseAllowlist: missing key %q (got %v)", key, got)
				}
			}
		})
	}
}

func TestEvaluate_ThresholdAndAllowlist(t *testing.T) {
	t.Parallel()

	threshold := testThreshold

	// Build a fixture stream covering: under-threshold pass, over-threshold
	// pass not allowlisted (violation), over-threshold pass allowlisted
	// (no violation), subtest event ignored even if over (subtest /
	// filter), package-level event ignored, fail action with elapsed,
	// skip action ignored.
	stream := strings.Join([]string{
		`{"Action":"run","Package":"pkg/a","Test":"TestFast"}`,
		`{"Action":"pass","Package":"pkg/a","Test":"TestFast","Elapsed":0.1}`,
		`{"Action":"run","Package":"pkg/a","Test":"TestSlowNotAllowed"}`,
		`{"Action":"pass","Package":"pkg/a","Test":"TestSlowNotAllowed","Elapsed":3.5}`,
		`{"Action":"run","Package":"pkg/a","Test":"TestSlowAllowed"}`,
		`{"Action":"pass","Package":"pkg/a","Test":"TestSlowAllowed","Elapsed":4.2}`,
		`{"Action":"run","Package":"pkg/b","Test":"TestSubtest"}`,
		`{"Action":"pass","Package":"pkg/b","Test":"TestSubtest/sub","Elapsed":99.0}`, // subtest, ignored
		`{"Action":"pass","Package":"pkg/b","Test":"TestSubtest","Elapsed":1.0}`,
		`{"Action":"fail","Package":"pkg/c","Test":"TestSlowFail","Elapsed":5.0}`, // fail counted
		`{"Action":"skip","Package":"pkg/c","Test":"TestSkipped","Elapsed":7.0}`,  // skip ignored
		`{"Action":"pass","Package":"pkg/a","Test":"","Elapsed":10.0}`,            // package-level ignored
		`{"Action":"output","Package":"pkg/a","Test":"TestFast","Output":"ok\n"}`, // output ignored
	}, "\n") + "\n"

	allowlist := map[string]struct{}{
		"pkg/a\tTestSlowAllowed": {},
	}

	got, err := evaluate(strings.NewReader(stream), threshold, allowlist)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	wantKeys := map[string]float64{
		"pkg/a\tTestSlowNotAllowed": 3.5,
		"pkg/c\tTestSlowFail":       5.0,
	}
	if len(got) != len(wantKeys) {
		t.Fatalf("evaluate: want %d violations, got %d (%+v)", len(wantKeys), len(got), got)
	}
	for _, v := range got {
		key := v.Package + "\t" + v.Test
		want, ok := wantKeys[key]
		if !ok {
			t.Errorf("evaluate: unexpected violation %+v", v)
			continue
		}
		if v.Elapsed != time.Duration(want*float64(time.Second)) {
			t.Errorf("evaluate: %s elapsed got %v want %vs", key, v.Elapsed, want)
		}
	}
}

func TestEvaluate_MalformedJSON(t *testing.T) {
	t.Parallel()
	_, err := evaluate(strings.NewReader("not-json\n"), testThreshold, nil)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("evaluate: want decode error, got %v", err)
	}
}

func TestEvaluate_EmptyStream(t *testing.T) {
	t.Parallel()
	got, err := evaluate(bytes.NewReader(nil), testThreshold, nil)
	if err != nil {
		t.Fatalf("evaluate: unexpected err on empty stream: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("evaluate: want 0 violations, got %d", len(got))
	}
}

func TestEvaluate_NilAllowlistMeansNoneAllowed(t *testing.T) {
	t.Parallel()
	stream := `{"Action":"pass","Package":"pkg/a","Test":"TestX","Elapsed":3.0}` + "\n"
	_ = nilAllowlistOverElapsed // documents the 3.0s magnitude used in stream
	got, err := evaluate(strings.NewReader(stream), testThreshold, nil)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("evaluate: want 1 violation, got %d", len(got))
	}
}

func TestRenderViolations_Format(t *testing.T) {
	t.Parallel()
	v := []violation{
		{Package: "pkg/a", Test: "TestB", Elapsed: renderViolationSlowElapsed},
		{Package: "pkg/a", Test: "TestA", Elapsed: renderViolationFastElapsed},
	}
	const customAllowlistPath = "custom/path/allowlist.txt"
	var buf bytes.Buffer
	renderViolations(&buf, v, testThreshold, customAllowlistPath)
	out := buf.String()

	// Header must include exact threshold and count.
	wantHeader := "slowgate: 2 test(s) exceeded 2s budget"
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], wantHeader) {
		t.Errorf("renderViolations: header want %q, got %q", wantHeader, lines[0])
	}

	// Violation rows are precisely formatted: `  SLOW <pkg> <test> <elapsed> > <threshold>`
	// Elapsed values are rounded to ms (`Elapsed.Round(time.Millisecond)`):
	// 2100ms → 2.1s, 3500ms → 3.5s.
	wantRowA := "  SLOW pkg/a TestA 2.1s > 2s\n"
	wantRowB := "  SLOW pkg/a TestB 3.5s > 2s\n"
	if !strings.Contains(out, wantRowA) {
		t.Errorf("renderViolations: missing exact TestA row %q in:\n%s", wantRowA, out)
	}
	if !strings.Contains(out, wantRowB) {
		t.Errorf("renderViolations: missing exact TestB row %q in:\n%s", wantRowB, out)
	}
	// Sort order: TestA < TestB.
	idxA := strings.Index(out, "TestA")
	idxB := strings.Index(out, "TestB")
	if idxA < 0 || idxB < 0 || idxA >= idxB {
		t.Errorf("renderViolations: rows not sorted (A=%d, B=%d):\n%s", idxA, idxB, out)
	}

	// Actionable footer must echo the caller-supplied path verbatim, not
	// hard-code defaultAllowlistPath; this lets developers running slowgate
	// against an alternate allowlist (e.g. a draft file) see the right hint.
	if !strings.Contains(out, customAllowlistPath) {
		t.Errorf("renderViolations: footer missing caller-supplied path %q:\n%s", customAllowlistPath, out)
	}
	if strings.Contains(out, "tools/slowgate/allowlist.txt") {
		t.Errorf("renderViolations: footer must not hard-code the project default when caller passed a path:\n%s", out)
	}
}
