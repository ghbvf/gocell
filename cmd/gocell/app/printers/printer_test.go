package printers

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateGolden, when set, rewrites the testdata/golden/<format>/<case>.<ext>
// files in place. Used to regenerate the corpus after intentional output
// changes; CI runs without the flag and asserts byte-equality.
var updateGolden = flag.Bool("update", false, "update golden files in testdata/")

// TestNew_DispatchesByFormat covers the format → printer mapping and the
// unknown-format error path. The test does not exercise output content —
// each concrete printer has its own dedicated golden test below.
func TestNew_DispatchesByFormat(t *testing.T) {
	tests := []struct {
		name      string
		format    string
		wantType  string
		wantError bool
	}{
		{"text", "text", "*printers.TextPrinter", false},
		{"json", "json", "*printers.JSONPrinter", false},
		{"sarif", "sarif", "*printers.SARIFPrinter", false},
		{"unknown returns error", "yaml", "", true},
		{"empty format returns error", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p, err := New(tt.format, &buf, "test-version")
			if tt.wantError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unknown format",
					"error must mention 'unknown format' so callers can pattern-match")
				assert.Contains(t, err.Error(), "supported formats are",
					"error must list the supported formats")
				assert.Nil(t, p)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, p)
			gotType := reflect.TypeOf(p).String()
			assert.Equal(t, tt.wantType, gotType, "wrong concrete printer type")
		})
	}
}

// TestSupportedFormats locks the canonical format list. Callers building
// --help text rely on the order being [text json sarif] (default first).
func TestSupportedFormats(t *testing.T) {
	assert.Equal(t, []string{"text", "json", "sarif"}, SupportedFormats())
}

// TestSortResults_Stable verifies the comparator orders by (severity, code,
// file presence, file, scope, line, column, field). The asserted ordering
// is what the three printers serialise; if it drifts, every golden file
// breaks at once — this test catches that before that cascade happens.
func TestSortResults_Stable(t *testing.T) {
	in := []governance.ValidationResult{
		{Code: "B", Severity: governance.SeverityWarning, File: "a.yaml", Line: 5},
		{Code: "A", Severity: governance.SeverityError, File: "z.yaml", Line: 1},
		{Code: "A", Severity: governance.SeverityError, File: "a.yaml", Line: 9},
		{Code: "A", Severity: governance.SeverityError, Scope: "project"},
		{Code: "A", Severity: governance.SeverityError, File: "a.yaml", Line: 9, Column: 3},
		{Code: "A", Severity: governance.SeverityError, File: "a.yaml", Line: 9, Column: 3, Field: "x"},
	}
	got := sortResults(in)

	// Errors first.
	for i, r := range got {
		if r.Severity == governance.SeverityWarning {
			// All preceding entries must be errors.
			for j := 0; j < i; j++ {
				assert.Equal(t, governance.SeverityError, got[j].Severity,
					"errors must precede warnings, but index %d is %v", j, got[j].Severity)
			}
			break
		}
	}

	// Within Code=A errors, file-anchored come before scope-only.
	scopeIdx := -1
	for i, r := range got {
		if r.Code == "A" && r.Scope == "project" {
			scopeIdx = i
			break
		}
	}
	require.NotEqual(t, -1, scopeIdx, "scope-only result missing")
	for i := 0; i < scopeIdx; i++ {
		if got[i].Code != "A" || got[i].Severity != governance.SeverityError {
			continue
		}
		assert.NotEmpty(t, got[i].File,
			"file-anchored A errors must precede scope-only A error at index %d", i)
	}

	// Sorting twice yields the same result (idempotent / deterministic).
	got2 := sortResults(got)
	assert.Equal(t, got, got2, "sortResults must be idempotent")
}

// TestSortResults_DoesNotMutateInput guards the documented contract that the
// caller can keep using the input slice afterwards.
func TestSortResults_DoesNotMutateInput(t *testing.T) {
	in := []governance.ValidationResult{
		{Code: "Z"},
		{Code: "A"},
	}
	before := make([]governance.ValidationResult, len(in))
	copy(before, in)
	_ = sortResults(in)
	assert.Equal(t, before, in, "input slice must not be mutated by sortResults")
}

// TestCountSeverities is a direct check on the helper; the JSON and text
// printers both depend on it.
func TestCountSeverities(t *testing.T) {
	in := []governance.ValidationResult{
		{Severity: governance.SeverityError},
		{Severity: governance.SeverityWarning},
		{Severity: governance.SeverityError},
		{Severity: ""}, // unknown — must not be counted as either
	}
	errs, warns := countSeverities(in)
	assert.Equal(t, 2, errs)
	assert.Equal(t, 1, warns)
}

// goldenCases enumerates the fixtures that all three printers must handle.
// Each case fans out to one .txt, .json, and .sarif golden file under
// testdata/golden/<format>/<name>.<ext>. Adding a case adds three goldens —
// regenerate with `go test ./cmd/gocell/app/printers/... -update`.
var goldenCases = []struct {
	name    string
	results []governance.ValidationResult
}{
	{
		name:    "zero_issues",
		results: nil,
	},
	{
		name: "single_error_full_position",
		results: []governance.ValidationResult{
			{
				Code:      "REF-01",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueRefNotFound,
				File:      "cells/accesscore/cell.yaml",
				Field:     "contractUsages[0].contractId",
				Message:   "contract 'auth.session.v1' not found",
				Line:      14,
				Column:    5,
			},
		},
	},
	{
		name: "single_error_no_line",
		results: []governance.ValidationResult{
			{
				Code:     "REF-02",
				Severity: governance.SeverityError,
				File:     "cells/accesscore/cell.yaml",
				Field:    "owner",
				Message:  "owner missing",
			},
		},
	},
	{
		name: "scope_only",
		results: []governance.ValidationResult{
			{
				Code:      "DEP-02",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueInvalid,
				Scope:     "project",
				Field:     "cells",
				Message:   "circular dependency detected",
			},
		},
	},
	{
		name: "mixed_errors_warnings",
		results: []governance.ValidationResult{
			{
				Code:     "ADV-01",
				Severity: governance.SeverityWarning,
				File:     "cells/x/cell.yaml",
				Field:    "owner.role",
				Message:  "owner role is recommended",
			},
			{
				Code:     "REF-01",
				Severity: governance.SeverityError,
				File:     "cells/x/cell.yaml",
				Field:    "id",
				Message:  "cell ID is required",
				Line:     2,
				Column:   1,
			},
			{
				Code:      "FMT-02",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueInvalid,
				Scope:     "project",
				Field:     "type",
				Message:   "cell type invalid",
			},
		},
	},
	{
		name: "field_empty",
		results: []governance.ValidationResult{
			{
				Code:     "TEST-01",
				Severity: governance.SeverityError,
				File:     "f.yaml",
				Line:     7,
				Message:  "no field set",
			},
		},
	},
	{
		name: "unicode_and_special_chars",
		results: []governance.ValidationResult{
			{
				Code:     "MSG-01",
				Severity: governance.SeverityError,
				File:     "cells/中文/cell.yaml",
				Line:     1,
				Column:   1,
				Field:    "名称",
				Message:  "包含 \"引号\" 和 换行\n以及 emoji 🚀",
			},
		},
	},
	{
		// HTML-flavored characters in the message must round-trip
		// unescaped in JSON / SARIF: validation rules legitimately
		// reference path templates like `<userId>` or comparison
		// operators (`<`, `>`, `&`). See SetEscapeHTML(false).
		name: "html_chars_in_message",
		results: []governance.ValidationResult{
			{
				Code:     "MSG-02",
				Severity: governance.SeverityError,
				File:     "cells/x/cell.yaml",
				Line:     1,
				Field:    "schema",
				Message:  "type <string> & ref <user/x> not allowed when count > 0",
			},
		},
	},
}

// TestGolden_Text fans the golden corpus through TextPrinter and compares to
// testdata/golden/text/<name>.txt.
func TestGolden_Text(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewTextPrinter(&buf)
			require.NoError(t, p.Print(tc.results))
			assertGolden(t, "text", tc.name+".txt", buf.Bytes())
		})
	}
}

// TestGolden_JSON fans through JSONPrinter. The outputs are also re-parsed
// to confirm they're valid JSON (golden equality alone wouldn't catch a
// malformed-but-stable corruption).
func TestGolden_JSON(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewJSONPrinter(&buf)
			require.NoError(t, p.Print(tc.results))
			assertGolden(t, "json", tc.name+".json", buf.Bytes())

			// Sanity: re-parse to confirm valid JSON regardless of golden.
			var parsed documentJSON
			require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed),
				"JSON output must be re-parseable")
			assert.NotNil(t, parsed.Issues, "issues key must always be present (never null)")
		})
	}
}

// TestGolden_SARIF fans through SARIFPrinter with a fixed toolVersion so the
// goldens don't drift with the build hash. Re-parses to confirm the output
// is valid JSON conforming to our local DTO.
func TestGolden_SARIF(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewSARIFPrinter(&buf, "test-1.0.0")
			require.NoError(t, p.Print(tc.results))
			assertGolden(t, "sarif", tc.name+".sarif", buf.Bytes())

			var parsed sarifLog
			require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed),
				"SARIF output must be re-parseable")
			assert.Equal(t, sarifVersion, parsed.Version)
			require.Len(t, parsed.Runs, 1, "exactly one run")
			assert.Equal(t, sarifToolName, parsed.Runs[0].Tool.Driver.Name)
			assert.Equal(t, "test-1.0.0", parsed.Runs[0].Tool.Driver.Version)
		})
	}
}

// TestSARIF_DedupRulesByCode covers the rule-array de-duplication: 50
// results with the same Code must produce exactly one rules[] entry.
// Catches a regression where buildSARIFRules forgets the seen-map.
func TestSARIF_DedupRulesByCode(t *testing.T) {
	const n = 50
	results := make([]governance.ValidationResult, n)
	for i := range results {
		results[i] = governance.ValidationResult{
			Code:     "REF-01",
			Severity: governance.SeverityError,
			File:     "cells/x/cell.yaml",
			Line:     i + 1,
			Message:  "duplicate code regression fixture",
		}
	}
	var buf bytes.Buffer
	require.NoError(t, NewSARIFPrinter(&buf, "test").Print(results))

	var parsed sarifLog
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	require.Len(t, parsed.Runs[0].Tool.Driver.Rules, 1,
		"50 results sharing one Code must collapse to one rules[] entry")
	assert.Equal(t, "REF-01", parsed.Runs[0].Tool.Driver.Rules[0].ID)
	assert.Len(t, parsed.Runs[0].Results, n,
		"results[] must keep all 50 entries; dedup only applies to rules[]")
}

// TestSARIF_ScopeOnly_OmitsLocations covers the rule that scope-anchored
// findings (no file) must produce a result without locations[] but with the
// scope baked into the message text.
func TestSARIF_ScopeOnly_OmitsLocations(t *testing.T) {
	results := []governance.ValidationResult{
		{
			Code:     "DEP-02",
			Severity: governance.SeverityError,
			Scope:    "project",
			Field:    "cells",
			Message:  "circular dependency detected",
		},
	}
	var buf bytes.Buffer
	require.NoError(t, NewSARIFPrinter(&buf, "test").Print(results))

	var parsed sarifLog
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	require.Len(t, parsed.Runs[0].Results, 1)
	assert.Empty(t, parsed.Runs[0].Results[0].Locations,
		"scope-only result must have no locations[]")
	assert.Contains(t, parsed.Runs[0].Results[0].Message.Text, "[scope: project]",
		"scope name must be visible in the message text")
}

// TestSARIF_RegionOmittedWhenLineUnknown: when Line==0, the printer must
// omit the region object entirely; including {startLine:0,...} would let
// SARIF viewers render a bogus "line 0" anchor.
func TestSARIF_RegionOmittedWhenLineUnknown(t *testing.T) {
	results := []governance.ValidationResult{
		{
			Code:     "REF-02",
			Severity: governance.SeverityError,
			File:     "cells/x/cell.yaml",
			Message:  "no position",
		},
	}
	var buf bytes.Buffer
	require.NoError(t, NewSARIFPrinter(&buf, "test").Print(results))

	var parsed sarifLog
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	require.Len(t, parsed.Runs[0].Results, 1)
	require.Len(t, parsed.Runs[0].Results[0].Locations, 1)
	assert.Nil(t, parsed.Runs[0].Results[0].Locations[0].PhysicalLocation.Region,
		"region must be nil when Line==0")
}

// TestSARIF_StartColumnDefaultsToOne: when Line>0 but Column==0, the printer
// must emit startColumn=1 because SARIF 2.1.0 requires startColumn >= 1
// whenever startLine is set. Catches a regression where Column would slip
// through as 0 and break SARIF schema validation.
func TestSARIF_StartColumnDefaultsToOne(t *testing.T) {
	results := []governance.ValidationResult{
		{
			Code:     "REF-03",
			Severity: governance.SeverityError,
			File:     "f.yaml",
			Line:     7,
			Message:  "line known, column unknown",
		},
	}
	var buf bytes.Buffer
	require.NoError(t, NewSARIFPrinter(&buf, "test").Print(results))

	var parsed sarifLog
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	region := parsed.Runs[0].Results[0].Locations[0].PhysicalLocation.Region
	require.NotNil(t, region)
	assert.Equal(t, 7, region.StartLine)
	assert.Equal(t, 1, region.StartColumn,
		"startColumn must default to 1 to satisfy SARIF schema when Column is unset")
}

// TestText_PrintFailFast covers the fail-fast variant of the text printer:
// only the first error is rendered, no banner, no warnings, no summary.
func TestText_PrintFailFast(t *testing.T) {
	results := []governance.ValidationResult{
		{Code: "ADV-01", Severity: governance.SeverityWarning, Message: "warn first"},
		{Code: "REF-01", Severity: governance.SeverityError, Message: "first error"},
		{Code: "REF-02", Severity: governance.SeverityError, Message: "second error"},
	}
	var buf bytes.Buffer
	require.NoError(t, NewTextPrinter(&buf).PrintFailFast(results))
	out := buf.String()

	assert.Contains(t, out, "REF-01")
	assert.Contains(t, out, "first error")
	assert.NotContains(t, out, "REF-02")
	assert.NotContains(t, out, "second error")
	assert.NotContains(t, out, "warn first")
	assert.NotContains(t, out, "ERRORS (")
	assert.NotContains(t, out, "Validation complete:")
}

// TestText_PrintFailFast_NoErrors: when no error is present the writer
// receives nothing.
func TestText_PrintFailFast_NoErrors(t *testing.T) {
	results := []governance.ValidationResult{
		{Code: "ADV-01", Severity: governance.SeverityWarning, Message: "warn"},
	}
	var buf bytes.Buffer
	require.NoError(t, NewTextPrinter(&buf).PrintFailFast(results))
	assert.Empty(t, strings.TrimSpace(buf.String()))
}

// TestJSON_DoesNotEscapeHTMLChars locks SetEscapeHTML(false): messages with
// `<`, `>`, `&` must round-trip as the raw bytes, not as < / > / &
// — otherwise jq output and SARIF viewer text become illegible. Pinning
// this so a future stylistic edit doesn't accidentally re-enable the
// default and silently degrade output quality.
func TestJSON_DoesNotEscapeHTMLChars(t *testing.T) {
	results := []governance.ValidationResult{
		{
			Code:     "X-1",
			Severity: governance.SeverityError,
			File:     "f.yaml",
			Message:  "type <string> & ref <user>",
		},
	}
	var buf bytes.Buffer
	require.NoError(t, NewJSONPrinter(&buf).Print(results))
	body := buf.String()
	assert.Contains(t, body, "<string>", "JSON output must keep < and > raw")
	assert.Contains(t, body, "& ref", "JSON output must keep & raw")
	assert.NotContains(t, body, "\\u003c", "must not escape < as \\u003c")
	assert.NotContains(t, body, "\\u003e", "must not escape > as \\u003e")
	assert.NotContains(t, body, "\\u0026", "must not escape & as \\u0026")
}

// TestSARIF_DoesNotEscapeHTMLChars same assertion for SARIF output. SARIF
// viewers (VS Code SARIF Explorer, GitHub Code Scanning) display
// message.text verbatim; HTML-escaping turns rule descriptions like
// `placeholder <userId>` into noise.
func TestSARIF_DoesNotEscapeHTMLChars(t *testing.T) {
	results := []governance.ValidationResult{
		{
			Code:     "X-2",
			Severity: governance.SeverityError,
			File:     "f.yaml",
			Message:  "placeholder <userId> & path /users/<id>",
		},
	}
	var buf bytes.Buffer
	require.NoError(t, NewSARIFPrinter(&buf, "test").Print(results))
	body := buf.String()
	assert.Contains(t, body, "<userId>")
	assert.Contains(t, body, "& path")
	assert.NotContains(t, body, "\\u003c")
	assert.NotContains(t, body, "\\u003e")
	assert.NotContains(t, body, "\\u0026")
}

// TestJSON_EmptyIssues_NotNull: zero results must serialise as
// "issues": [] (never null) so consumers can iterate without nil-guarding.
func TestJSON_EmptyIssues_NotNull(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, NewJSONPrinter(&buf).Print(nil))
	body := buf.String()
	assert.Contains(t, body, `"issues": []`)
	assert.NotContains(t, body, `"issues": null`,
		"empty issues must serialise as [] not null")
}

// assertGolden compares actual output bytes to the golden file at
// testdata/golden/<format>/<filename>. With -update, rewrites the file in
// place; without it, asserts byte-for-byte equality and prints a unified
// diff hint when they differ.
func assertGolden(t *testing.T, format, filename string, actual []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", format, filename)
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, actual, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\n(hint: run with `-update` to create it)", path, err)
	}
	if !bytes.Equal(want, actual) {
		t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s\n(hint: run with `-update` to refresh)",
			path, want, actual)
	}
}
