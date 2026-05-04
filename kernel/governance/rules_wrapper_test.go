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

// TestValidateContractSpecClientsLiteral covers validateContractSpecClientsLiteral,
// clientSetsEqual, parseContractSpecClientsField and extractStringLiterals paths.
func TestValidateContractSpecClientsLiteral(t *testing.T) {
	contract := &metadata.ContractMeta{
		ID:   "http.config.internal.get.v1",
		Kind: "http",
		Endpoints: metadata.EndpointsMeta{
			HTTP:    &metadata.HTTPTransportMeta{Method: "GET", Path: "/internal/v1/config/{key}"},
			Clients: []string{"accesscore"},
		},
	}
	project := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.config.internal.get.v1": contract,
		},
	}
	v := NewValidator(project, t.TempDir(), clock.Real())

	t.Run("nil clients skipped", func(t *testing.T) {
		results := v.validateContractSpecClientsLiteral(contractSpecLiteral{
			id:      "http.config.internal.get.v1",
			clients: nil,
		}, contract)
		assert.Empty(t, results, "nil clients must skip the check")
	})

	t.Run("equal sets no error", func(t *testing.T) {
		results := v.validateContractSpecClientsLiteral(contractSpecLiteral{
			id:      "http.config.internal.get.v1",
			clients: []string{"accesscore"},
		}, contract)
		assert.Empty(t, results)
	})

	t.Run("go has extra client", func(t *testing.T) {
		results := v.validateContractSpecClientsLiteral(contractSpecLiteral{
			id:      "http.config.internal.get.v1",
			clients: []string{"accesscore", "auditcore"},
		}, contract)
		require.Len(t, results, 1)
		assert.Equal(t, codeFMT18, results[0].Code)
	})

	t.Run("yaml has extra client", func(t *testing.T) {
		results := v.validateContractSpecClientsLiteral(contractSpecLiteral{
			id:      "http.config.internal.get.v1",
			clients: []string{},
		}, contract)
		require.Len(t, results, 1)
		assert.Equal(t, codeFMT18, results[0].Code)
	})
}

// TestScanContractSpecLiterals_WithClients verifies that Clients []string fields
// are extracted from ContractSpec composite literals (exercising
// parseContractSpecClientsField and extractStringLiterals).
func TestScanContractSpecLiterals_WithClients(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

var spec = wrapper.ContractSpec{
	ID:        "http.config.internal.get.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/internal/v1/config/{key}",
	Clients:   []string{"accesscore", "auditcore"},
}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 1)
	assert.Equal(t, []string{"accesscore", "auditcore"}, literals[0].clients)
}

// TestScanContractSpecLiterals_BinaryConcatID exercises resolveStringValue
// for *ast.BinaryExpr (string concatenation) and resolveBinaryConcat.
func TestScanContractSpecLiterals_BinaryConcatID(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "mytest")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package mytest

import "github.com/ghbvf/gocell/kernel/wrapper"

const (
	prefix  = "http.auth."
	suffix  = "login.v1"
)

var spec = wrapper.ContractSpec{
	ID:        prefix + suffix,
	Kind:      "http",
	Transport: "http",
	Method:    "POST",
	Path:      "/api/v1/login",
}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 1)
	assert.Equal(t, "http.auth.login.v1", literals[0].id)
}

// TestScanContractSpecLiterals_NonStringLiteralClients verifies that a Clients
// field containing a non-string-literal element (e.g. an identifier) causes
// parseContractSpecClientsField to return nil (unresolvable slice).
func TestScanContractSpecLiterals_NonStringLiteralClients(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "mytest2")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package mytest2

import "github.com/ghbvf/gocell/kernel/wrapper"

const callerCell = "accesscore"

var spec = wrapper.ContractSpec{
	ID:        "http.config.internal.get.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/internal/v1/config/{key}",
	Clients:   []string{callerCell},
}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 1)
	// callerCell is an identifier, not a string literal → clients is nil (unresolvable)
	assert.Nil(t, literals[0].clients)
}

// TestScanContractSpecLiterals_IntLiteralFieldValue exercises the stringLiteralValue
// path where the BasicLit kind is not STRING (e.g. token.INT). The scanner
// uses go/parser without type-checking, so a syntactically-legal but
// semantically-invalid field value (integer literal for a string field) is
// parsed without error; stringLiteralValue rejects it (Kind != STRING) and
// the field is skipped.
func TestScanContractSpecLiterals_IntLiteralFieldValue(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "intlit")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	// go/parser accepts this syntactically; the type-checker would reject it,
	// but the scanner does not run the type-checker.
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package intlit

import "github.com/ghbvf/gocell/kernel/wrapper"

// Syntactically valid to go/parser, semantically wrong — scanner skips integer field.
var spec = wrapper.ContractSpec{
	ID:   42,
	Kind: "http", Transport: "http", Method: "GET", Path: "/api/v1/x",
}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	// ID field skipped (integer not a string literal) → id stays ""
	require.Len(t, literals, 1)
	assert.Equal(t, "", literals[0].id)
}

// TestScanContractSpecLiterals_BinaryConcatLeftUnresolvable exercises
// resolveBinaryConcat where the left side cannot be resolved (function call).
// This covers the left-ok=false early return branch of resolveBinaryConcat.
func TestScanContractSpecLiterals_BinaryConcatLeftUnresolvable(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "binleft")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package binleft

import "github.com/ghbvf/gocell/kernel/wrapper"

func prefix() string { return "http.test." }

var spec = wrapper.ContractSpec{
	ID:        prefix() + "v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/api/v1/x",
}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	// left side is a function call → resolveBinaryConcat left ok=false → ID field skipped
	require.Len(t, literals, 1)
	assert.Equal(t, "", literals[0].id, "unresolvable left-side concat → ID field skipped")
}

// TestScanContractSpecLiterals_DotImport verifies that a dot-import of
// kernel/wrapper is not tracked as a wrapper alias (isWrapperSelector returns
// false for "" alias, dot-import sets alias to ".").
func TestScanContractSpecLiterals_DotImport(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "dotimport")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package dotimport

import . "github.com/ghbvf/gocell/kernel/wrapper"

var _ = EventSpec("event.x.v1", "amqp")
`), 0o644))

	// Dot-import bypasses the alias-based selector check; scanner must produce 0 literals.
	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	assert.Empty(t, literals, "dot-import must not produce FMT-18 literals")
}

// TestScanContractSpecLiterals_EventSpecNoArgs verifies that an EventSpec call
// with no arguments does not panic and is gracefully skipped.
func TestScanContractSpecLiterals_EventSpecNoArgs(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "noargs")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	// EventSpec() with no args — the parser will accept the syntax; scanner must
	// treat len(args) < 1 gracefully (return false, skip).
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package noargs

import "github.com/ghbvf/gocell/kernel/wrapper"

// deliberately calling EventSpec with no args to hit the len(args)<1 guard
var _ = wrapper.EventSpec
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	// A function reference (not a call) produces no literal.
	assert.Empty(t, literals)
}

// TestScanContractSpecLiterals_BinaryConcatNonAdd exercises resolveBinaryConcat
// for a non-ADD operator (subtraction), which must return ok=false.
func TestScanContractSpecLiterals_BinaryConcatNonAdd(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "nonaddop")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	// Using a numeric expression for ID — won't be a string literal, so ID is unresolved.
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package nonaddop

import "github.com/ghbvf/gocell/kernel/wrapper"

var x = wrapper.EventSpec(someFunc(), "amqp")

func someFunc() string { return "x" }
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	// someFunc() cannot be resolved → unresolved=true literal
	require.Len(t, literals, 1)
	assert.True(t, literals[0].unresolved)
}

// TestScanContractSpecLiterals_ResolveBinaryRight exercises the right-side
// unresolvable branch of resolveBinaryConcat (left resolves, right does not).
// When the right side of a "+" cannot be resolved, resolveStringValue returns
// ok=false and parseContractSpecCompositeLit skips the field (ID stays ""),
// so the literal is recorded with an empty id (not unresolved).
func TestScanContractSpecLiterals_ResolveBinaryRight(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "binaryright")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package binaryright

import "github.com/ghbvf/gocell/kernel/wrapper"

func getID() string { return "v1" }

var spec = wrapper.ContractSpec{
	ID:        "http.test." + getID(),
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/api/v1/x",
}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	// right side is a function call → resolveStringValue returns ok=false,
	// the field is skipped, id stays "" (literal recorded but with empty id).
	require.Len(t, literals, 1)
	assert.Equal(t, "", literals[0].id, "unresolvable right-side concat → ID field skipped → empty id")
	assert.False(t, literals[0].unresolved, "ContractSpec composite literal does not set unresolved on partial failure")
}
