package governance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

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
