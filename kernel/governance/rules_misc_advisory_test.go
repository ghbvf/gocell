package governance

// rules_misc_advisory_test.go consolidates tests for the SLICE-CONSISTENCY-01,
// FMT-19 (wrapper package-state), and DOC-NAME-01 rules merged into
// rules_misc_advisory.go. The advisory (ADV-01..06) and OUTGUARD-01 source
// rules have no per-rule test files in this PR; their coverage runs through
// validate_test.go integration suites.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// =============================================================================
// SLICE-CONSISTENCY-01 (formerly rules_slice_test.go)
// =============================================================================

func TestValidateSliceConsistency(t *testing.T) {
	tests := []struct {
		name           string
		cellLevel      string
		sliceLevel     string
		wantErrorCount int
		wantCode       string
	}{
		{
			name:           "slice with no explicit level inherits cell - 0 findings",
			cellLevel:      "L2",
			sliceLevel:     "",
			wantErrorCount: 0,
		},
		{
			name:           "slice level equals cell level - 0 findings",
			cellLevel:      "L2",
			sliceLevel:     "L2",
			wantErrorCount: 0,
		},
		{
			name:           "slice downgrade L1 in L2 cell - 0 findings",
			cellLevel:      "L2",
			sliceLevel:     "L1",
			wantErrorCount: 0,
		},
		{
			name:           "slice L0 in L3 cell - 0 findings",
			cellLevel:      "L3",
			sliceLevel:     "L0",
			wantErrorCount: 0,
		},
		{
			name:           "slice upgrades L3 in L2 cell - 1 error",
			cellLevel:      "L2",
			sliceLevel:     "L3",
			wantErrorCount: 1,
			wantCode:       "SLICE-CONSISTENCY-01",
		},
		{
			name:           "slice empty string treated as inherit - 0 findings",
			cellLevel:      "L1",
			sliceLevel:     "",
			wantErrorCount: 0,
		},
		{
			name:           "slice invalid level L9 - 1 error",
			cellLevel:      "L2",
			sliceLevel:     "L9",
			wantErrorCount: 1,
			wantCode:       "SLICE-CONSISTENCY-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					"testcell": {
						ID:               "testcell",
						ConsistencyLevel: tt.cellLevel,
					},
				},
				Slices: map[string]*metadata.SliceMeta{
					"testcell/testslice": {
						ID:               "testslice",
						BelongsToCell:    "testcell",
						ConsistencyLevel: tt.sliceLevel,
					},
				},
				Contracts:  map[string]*metadata.ContractMeta{},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}
			v := NewValidator(project, ".", clock.Real())
			results := v.validateSliceConsistency()

			var errCount int
			for _, r := range results {
				if r.Severity == SeverityError {
					errCount++
					if tt.wantCode != "" {
						assert.Equal(t, tt.wantCode, r.Code)
					}
				}
			}
			assert.Equal(t, tt.wantErrorCount, errCount, "unexpected error count")
		})
	}
}

// TestValidateSliceConsistency_MissingParentCell verifies that a slice with
// no registered parent cell is skipped (REF-01 covers the missing cell).
func TestValidateSliceConsistency_MissingParentCell(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{
			"ghostcell/testslice": {
				ID:               "testslice",
				BelongsToCell:    "ghostcell",
				ConsistencyLevel: "L3",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(project, ".", clock.Real())
	results := v.validateSliceConsistency()
	assert.Empty(t, results, "missing parent cell should be silently skipped")
}

// =============================================================================
// FMT-19 wrapper package-state scanner (formerly rules_wrapper_test.go)
// =============================================================================

// FMT-18 (wrapper.ContractSpec literal cross-check against contracts/**/contract.yaml)
// was deleted in PR-V1-CODEGEN-FULL-MIGRATION. After W3 cells/** contains 0
// ContractSpec literals — enforced statically by archtest gates:
//   - CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01
//   - NO-MANUAL-CONTRACTSPEC-LITERAL-01
//   - EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
//
// All FMT-18 unit tests (TestScanContractSpecLiterals*, TestValidateContractSpecLiteral*,
// TestValidateContractSpecClientsLiteral) have been removed alongside the rule.

func TestValidateFMT19WrapperPackageState(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		wantErrors int
		wantText   string // substring that must appear in any error (empty = no assertion)
	}{
		// Rule ①: blank-identifier compile-time interface check — always accepted.
		{
			name:       "blank-ident interface check",
			source:     `var _ Tracer = NoopTracer{}`,
			wantErrors: 0,
		},
		// Rule ②: single-name, zero-element struct composite literal — accepted.
		{
			name:       "zero-value composite literal explicit type",
			source:     `var zero NoopTracer = NoopTracer{}`,
			wantErrors: 0,
		},
		// Rule ②: implicit-type form `var z = Type{}` — accepted.
		{
			name:       "zero-value composite literal implicit type",
			source:     `var z = NoopTracer{}`,
			wantErrors: 0,
		},
		// The two actual production vars in kernel/wrapper/tracer.go.
		{
			name: "tracer production vars",
			source: `var _ Tracer = NoopTracer{}
var noopSpanInstance Span = noopSpan{}`,
			wantErrors: 0,
		},
		// Rule ②: selector-expr zero-value composite literal — accepted.
		// Covers `var x pkg.Type = pkg.Type{}` where the type is *ast.SelectorExpr.
		// The test framework prepends "package wrapper\n\n" to the source; the
		// import is syntactically parsed without resolution (SkipObjectResolution).
		{
			name:       "selector-expr zero-value composite literal",
			source:     `import "example.com/pkg"` + "\n\n" + `var x pkg.Type = pkg.Type{}`,
			wantErrors: 0,
		},
		// Violations — nil RHS.
		{
			name:       "interface var with nil",
			source:     `var globalTracer Tracer = nil`,
			wantErrors: 1,
			wantText:   "globalTracer",
		},
		{
			name:       "pointer var with nil",
			source:     `var globalSpan *span = nil`,
			wantErrors: 1,
			wantText:   "globalSpan",
		},
		// Violation — no initializer.
		{
			name:       "no initializer",
			source:     `var naked Tracer`,
			wantErrors: 1,
			wantText:   "naked",
		},
		// Violation — grouped block with two nil violations.
		{
			name: "grouped block with violations",
			source: `var (
	a Tracer = nil
	b *span = nil
)`,
			wantErrors: 2,
		},
		// Violation — multi-name declaration (even with nil RHS).
		{
			name:       "multi-name declaration",
			source:     `var a, b Tracer = nil, nil`,
			wantErrors: 1,
		},
		// Violation — chan type.
		{
			name:       "chan type mutable container",
			source:     `var q chan Tracer = make(chan Tracer)`,
			wantErrors: 1,
			wantText:   "q",
		},
		// Violation — map empty literal (reference type even when empty).
		{
			name:       "map type empty literal",
			source:     `var m map[string]Tracer = map[string]Tracer{}`,
			wantErrors: 1,
			wantText:   "m",
		},
		// Violation — slice empty literal (reference type).
		{
			name:       "slice type empty literal",
			source:     `var s []Tracer = []Tracer{}`,
			wantErrors: 1,
			wantText:   "s",
		},
		// Violation — function call RHS.
		{
			name:       "function call RHS",
			source:     `var t = buildTracer()`,
			wantErrors: 1,
			wantText:   "t",
		},
		// Violation — identifier reference RHS (base has no initializer, derived refs it).
		{
			name: "ident reference RHS and no-initializer",
			source: `var base NoopTracer
var derived = base`,
			wantErrors: 2,
		},
		// Non-empty composite literal — rejected.
		{
			name:       "non-empty composite literal",
			source:     `var t = NoopTracer{field: 1}`,
			wantErrors: 1,
			wantText:   "t",
		},
		// PR246-FU1 reverse cases — A4 expanded matrix.
		// Parenthesised composite literal `var x = (T{})` — accepted (ParenExpr unwraps).
		{
			name:       "parenthesised zero-value composite literal",
			source:     `var paren = (NoopTracer{})`,
			wantErrors: 0,
		},
		// Doubly parenthesised — accepted (loop unwrap).
		{
			name:       "doubly parenthesised zero-value composite literal",
			source:     `var dparen = ((NoopTracer{}))`,
			wantErrors: 0,
		},
		// Pointer-of-composite `&T{}` — rejected (UnaryExpr.X = CompositeLit, not bare composite).
		{
			name:       "pointer to zero-value composite literal",
			source:     `var ptr = &NoopTracer{}`,
			wantErrors: 1,
			wantText:   "ptr",
		},
		// Function literal RHS — rejected.
		{
			name:       "function literal RHS",
			source:     `var fn = func() Tracer { return NoopTracer{} }`,
			wantErrors: 1,
			wantText:   "fn",
		},
		// Mixed var block — first is valid blank-ident, second is illegal nil.
		{
			name: "mixed var block: blank-ident OK, nil violation",
			source: `var (
	_ Tracer = NoopTracer{}
	bad Tracer = nil
)`,
			wantErrors: 1,
			wantText:   "bad",
		},
		// Pointer-typed composite literal `var x = (*T)(nil)` — rejected (CallExpr).
		{
			name:       "type conversion to pointer-nil",
			source:     `var typed = (*Tracer)(nil)`,
			wantErrors: 1,
			wantText:   "typed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runFMT19Case(t, tc.source, tc.wantErrors, tc.wantText)
		})
	}
}

// runFMT19Case writes a synthetic kernel/wrapper package, runs validateFMT19,
// and asserts the result count + message contents. Extracted from the table
// loop to keep the test runner's cognitive complexity within the project
// guard (≤ 15) per kernel/wrapper FMT-19 expectations on its own surface.
func runFMT19Case(t *testing.T, source string, wantErrors int, wantText string) {
	t.Helper()
	root := t.TempDir()
	wrapperDir := filepath.Join(root, "kernel", "wrapper")
	require.NoError(t, os.MkdirAll(wrapperDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "state.go"),
		[]byte("package wrapper\n\n"+source+"\n"), 0o644))
	// Violations inside _test.go must be ignored by the scanner.
	require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "state_test.go"),
		[]byte("package wrapper\nvar ignored Tracer = nil\n"), 0o644))

	results := NewValidator(&metadata.ProjectMeta{}, root, clock.Real()).validateFMT19(true)
	assert.Len(t, results, wantErrors, "got results: %v", results)
	for _, r := range results {
		assert.Equal(t, codeFMT19, r.Code)
	}
	if wantText != "" && len(results) > 0 {
		assertResultsContain(t, results, wantText)
	}
}

func assertResultsContain(t *testing.T, results []ValidationResult, want string) {
	t.Helper()
	for _, r := range results {
		if strings.Contains(r.Message, want) {
			return
		}
	}
	t.Errorf("expected %q in any result message, got: %v", want, results)
}

// =============================================================================
// DOC-NAME-01 (formerly rules_docs_test.go)
// =============================================================================

func TestValidateDOCNAME01_StrictScansActiveDocs(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "# Demo\nUse sso-bff here.\n")
	writeFile(t, root, "docs/reviews/old.md", "Historical sso-bff is allowed here.\n")
	writeFile(t, root, "templates/adr.md", "Use ssobff here.\n")

	v := NewValidator(validProject(), root, clock.Real())
	results := v.validateDOCNAME01(true)

	require.Len(t, results, 1)
	got := results[0]
	assert.Equal(t, "DOC-NAME-01", got.Code)
	assert.Equal(t, SeverityError, got.Severity)
	assert.Equal(t, IssueForbidden, got.IssueType)
	assert.Equal(t, "README.md", got.File)
	assert.Equal(t, "content", got.Field)
	assert.Equal(t, 2, got.Line)
	assert.Positive(t, got.Column)
	assert.Contains(t, got.Message, `"sso-bff"`)
	assert.Contains(t, got.Message, `"ssobff"`)
}

func TestValidateDOCNAME01_NonStrictSilent(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")

	v := NewValidator(validProject(), root, clock.Real())
	assert.Empty(t, v.validateDOCNAME01(false))
}

func TestValidateDOCNAME01_MissingGuardIsStrictError(t *testing.T) {
	root := t.TempDir()

	v := NewValidator(validProject(), root, clock.Real())
	results := v.validateDOCNAME01(true)

	require.Len(t, results, 1)
	assert.Equal(t, "DOC-NAME-01", results[0].Code)
	assert.Equal(t, SeverityError, results[0].Severity)
	assert.Equal(t, IssueRequired, results[0].IssueType)
	assert.Equal(t, docNamingGuardRelPath, results[0].File)
}

func TestValidateDOCNAME01_GlobIncludeAndWordBoundary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - examples/*/README.md
exclude: []
replacements:
  - literal: todo-order
    replacement: todoorder
`)
	writeFile(t, root, "examples/todoorder/README.md", "todo-order should fail; todo-orderly should not.\n")
	writeFile(t, root, "examples/ssobff/README.md", "todoorder is already clean.\n")

	v := NewValidator(validProject(), root, clock.Real())
	results := v.validateDOCNAME01(true)

	require.Len(t, results, 1)
	assert.Equal(t, "examples/todoorder/README.md", results[0].File)
	assert.Equal(t, 1, results[0].Line)
	assert.Equal(t, 1, results[0].Column)
}

func TestValidateDOCNAME01_InvalidGuardConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		wantIssue   IssueType
		wantField   string
		wantMessage string
	}{
		{
			name:        "bad YAML",
			config:      "include: [\n",
			wantIssue:   IssueInvalid,
			wantField:   "",
			wantMessage: "cannot parse",
		},
		{
			name: "missing include",
			config: `
replacements:
  - literal: sso-bff
    replacement: ssobff
`,
			wantIssue:   IssueRequired,
			wantField:   "include",
			wantMessage: "include pattern",
		},
		{
			name: "missing replacements",
			config: `
include:
  - README.md
`,
			wantIssue:   IssueRequired,
			wantField:   "replacements",
			wantMessage: "replacement",
		},
		{
			name: "empty literal",
			config: `
include:
  - README.md
replacements:
  - literal: ""
    replacement: ssobff
`,
			wantIssue:   IssueRequired,
			wantField:   "replacements[0]",
			wantMessage: "literal and replacement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "docs/architecture/naming-guard.yaml", tt.config)

			v := NewValidator(validProject(), root, clock.Real())
			results := v.validateDOCNAME01(true)

			require.NotEmpty(t, results)
			assert.Equal(t, "DOC-NAME-01", results[0].Code)
			assert.Equal(t, tt.wantIssue, results[0].IssueType)
			assert.Equal(t, tt.wantField, results[0].Field)
			assert.Contains(t, results[0].Message, tt.wantMessage)
		})
	}
}

func TestValidateDOCNAME01_InvalidIncludeAndReadError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - "["
  - README.md
replacements:
  - literal: sso-bff
    replacement: ssobff
`)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")
	v := NewValidator(validProject(), root, clock.Real())

	results := v.validateDOCNAME01(true)
	require.Len(t, results, 1)
	assert.Equal(t, IssueInvalid, results[0].IssueType)
	assert.Contains(t, results[0].Message, "invalid document naming include pattern")

	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - README.md
replacements:
  - literal: sso-bff
    replacement: ssobff
`)
	v = NewValidator(validProject(), root, clock.Real())
	v.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(filepath.ToSlash(path), "/README.md") {
			return nil, errors.New("permission denied")
		}
		return os.ReadFile(filepath.Clean(path))
	}

	results = v.validateDOCNAME01(true)
	require.Len(t, results, 1)
	assert.Equal(t, "README.md", results[0].File)
	assert.Equal(t, IssueInvalid, results[0].IssueType)
	assert.Contains(t, results[0].Message, "cannot read active document")
}

func TestValidateDOCNAME01_EmptyRootSilent(t *testing.T) {
	v := NewValidator(validProject(), "", clock.Real())
	assert.Empty(t, v.validateDOCNAME01(true))
}

func TestDocNamingPatternMatch(t *testing.T) {
	assert.True(t, docNamingPatternMatch("docs/design/a.md", "docs/design/**"))
	assert.True(t, docNamingPatternMatch("examples/todoorder/README.md", "examples/*/README.md"))
	assert.True(t, docNamingPatternMatch("README.md", "README.md"))
	assert.False(t, docNamingPatternMatch("docs/plans/a.md", "docs/design/**"))
	assert.False(t, docNamingPatternMatch("docs/design/a.md", "["))
}

func TestValidateStrict_IncludesDOCNAME01(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")

	v := NewValidator(emptyDocNamingProject(), root, clock.Real())
	results, err := v.ValidateStrict(t.Context(), true)
	require.NoError(t, err)
	assertDOCNAME01Present(t, results)
}

func TestValidateStrictFailFast_IncludesDOCNAME01(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")

	v := NewValidator(emptyDocNamingProject(), root, clock.Real())
	results, err := v.ValidateStrictFailFast(t.Context())
	require.NoError(t, err)

	require.Len(t, results, 1)
	assertDOCNAME01Present(t, results)
}

func emptyDocNamingProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func assertDOCNAME01Present(t *testing.T, results []ValidationResult) {
	t.Helper()
	for _, r := range results {
		if r.Code == "DOC-NAME-01" {
			assert.Equal(t, SeverityError, r.Severity)
			return
		}
	}
	t.Fatalf("expected DOC-NAME-01 in %v", results)
}

func writeDocNamingGuard(t *testing.T, root string) {
	t.Helper()
	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - README.md
  - docs/reviews/**
  - templates/**
exclude:
  - docs/reviews/**
replacements:
  - literal: sso-bff
    replacement: ssobff
`)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
