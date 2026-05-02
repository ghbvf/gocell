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

func TestScanContractSpecLiterals(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

var spec = wrapper.ContractSpec{
	ID:        "http.auth.login.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "POST",
	Path:      "/api/v1/access/sessions/login",
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes_test.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

var ignored = wrapper.ContractSpec{ID: "test.only", Kind: "http"}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 1)
	assert.Equal(t, "http.auth.login.v1", literals[0].id)
	assert.Equal(t, "http", literals[0].kind)
	assert.Equal(t, "POST", literals[0].method)
	assert.Equal(t, "/api/v1/access/sessions/login", literals[0].path)
}

func TestValidateContractSpecLiteral(t *testing.T) {
	project := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:   "http.auth.login.v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method: "POST",
						Path:   "/api/v1/access/sessions/login",
					},
				},
			},
		},
	}
	v := NewValidator(project, t.TempDir(), clock.Real())

	assert.Empty(t, v.validateContractSpecLiteral(contractSpecLiteral{
		file:   "cells/accesscore/routes.go",
		line:   10,
		id:     "http.auth.login.v1",
		kind:   "http",
		method: "POST",
		path:   "/api/v1/access/sessions/login",
	}))

	missing := v.validateContractSpecLiteral(contractSpecLiteral{
		file: "cells/accesscore/routes.go",
		line: 11,
		id:   "http.auth.missing.v1",
	})
	require.Len(t, missing, 1)
	assert.Equal(t, codeFMT18, missing[0].Code)

	mismatch := v.validateContractSpecLiteral(contractSpecLiteral{
		file:   "cells/accesscore/routes.go",
		line:   12,
		id:     "http.auth.login.v1",
		kind:   "event",
		method: "GET",
		path:   "/wrong",
	})
	require.Len(t, mismatch, 3)
}

// TestScanContractSpecLiterals_EventSpecCall verifies FMT-18 picks up the
// wrapper.EventSpec("id", "transport") helper-constructor form so ID literals
// passed via the helper participate in the YAML cross-check.
func TestScanContractSpecLiterals_EventSpecCall(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

var spec = wrapper.EventSpec("event.role.assigned.v1", "amqp")
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 1)
	assert.Equal(t, "event.role.assigned.v1", literals[0].id)
	assert.Equal(t, "event", literals[0].kind)
	assert.Equal(t, "event.role.assigned.v1", literals[0].topic)
}

// TestScanContractSpecLiterals_ResolvesStringConst verifies that
// wrapper.ContractSpec{...} literals whose field values reference
// package-level string constants are resolved at scan time — so both
// `Path: "/api/v1/..."` and `Path: pathUserByID` flow through the same
// validation, preventing the pre-F1 escape hatch where constant
// references silently bypassed the YAML cross-check.
func TestScanContractSpecLiterals_ResolvesStringConst(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

const (
	pathUserByID = "/api/v1/access/users/{id}"
	TopicUserCreated = "event.user.created.v1"
)

var (
	specGet = wrapper.ContractSpec{
		ID: "http.auth.user.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: pathUserByID,
	}
	specEvent = wrapper.ContractSpec{
		ID: TopicUserCreated, Kind: "event", Transport: "amqp",
		Topic: TopicUserCreated,
	}
	specCall = wrapper.EventSpec(TopicUserCreated, "amqp")
)
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 3)

	// Struct literal with path resolved via const.
	assert.Equal(t, "http.auth.user.get.v1", literals[0].id)
	assert.Equal(t, "/api/v1/access/users/{id}", literals[0].path)

	// Struct literal with both ID and Topic via const.
	assert.Equal(t, "event.user.created.v1", literals[1].id)
	assert.Equal(t, "event.user.created.v1", literals[1].topic)

	// EventSpec call with const identifier.
	assert.Equal(t, "event.user.created.v1", literals[2].id)
	assert.Equal(t, "event", literals[2].kind)
	assert.Equal(t, "event.user.created.v1", literals[2].topic)
}

// TestScanContractSpecLiterals_HonoursImportAlias verifies that FMT-18
// discovers wrapper.ContractSpec / wrapper.EventSpec even when the file
// imports kernel/wrapper under a non-default local name, e.g.
//
//	import kw "github.com/ghbvf/gocell/kernel/wrapper"
//
// Pre-F-2 the scanner hard-coded `pkg.Name == "wrapper"` and silently
// skipped alias forms, a governance escape hatch.
func TestScanContractSpecLiterals_HonoursImportAlias(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import kw "github.com/ghbvf/gocell/kernel/wrapper"

var (
	specHTTP  = kw.ContractSpec{ID: "http.auth.login.v1", Kind: "http", Transport: "http", Method: "POST", Path: "/x"}
	specEvent = kw.EventSpec("event.aliased.v1", "amqp")
)
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 2)
	assert.Equal(t, "http.auth.login.v1", literals[0].id)
	assert.Equal(t, "event.aliased.v1", literals[1].id)
}

// TestScanContractSpecLiterals_SkipsFilesWithoutWrapperImport ensures the
// scanner short-circuits non-wrapper files so an accidentally-matching
// `somepkg.ContractSpec{...}` literal in an unrelated file never produces
// a false positive.
func TestScanContractSpecLiterals_SkipsFilesWithoutWrapperImport(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "example.com/other/wrapper"

var spec = wrapper.ContractSpec{ID: "imposter.v1", Kind: "http", Transport: "http", Method: "GET", Path: "/x"}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	assert.Empty(t, literals, "non-kernel/wrapper import must not produce FMT-18 literals")
}

// TestValidateContractSpecLiteral_UnresolvedWarns verifies the F-3 fix:
// EventSpec/ContractSpec invocations whose ID cannot be resolved to a
// string literal produce a visible FMT-18 error instead of being silently
// dropped.
func TestValidateContractSpecLiteral_UnresolvedWarns(t *testing.T) {
	v := NewValidator(&metadata.ProjectMeta{}, t.TempDir(), clock.Real())
	results := v.validateContractSpecLiteral(contractSpecLiteral{
		file:       "cells/mystery/mystery.go",
		line:       42,
		kind:       "event",
		unresolved: true,
	})
	require.Len(t, results, 1)
	assert.Equal(t, codeFMT18, results[0].Code)
	assert.Contains(t, results[0].Message, "could not be resolved")
}

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
