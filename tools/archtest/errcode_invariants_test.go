//go:build archtest

package archtest

// errcode_invariants_test.go consolidates errcode-theme invariants:
//   - INVARIANT: ERRCODE-KIND-LITERAL-01
//   - INVARIANT: ERRCODE-CARVEOUT-ADR-CONSISTENCY-01
//   - INVARIANT: MESSAGE-CONST-LITERAL-01
//   - INVARIANT: ERROR-FIRST-API-01
//   - INVARIANT: ERROR-FIRST-TYPED-NIL-01
//   - INVARIANT: EXPORTED-ERROR-NEW-01
//   - INVARIANT: DETAILS-SEALED-FIELD-FROZEN-01
//   - INVARIANT: ERRCODE-PREFIX-OWNERSHIP-01
//
// DETAILS-SLOG-ATTR-01 retired by PR #1035: sealed PublicDetail newtype
// (pkg/errcode/details.go) makes wire-unsafe construction inexpressible
// in Go's type system. See ADR docs/architecture/202605051730-adr-errcode-message-pii-safety.md.
// DETAILS-SEALED-FIELD-FROZEN-01 is the reflect-based field lock that
// guards the same invariant against in-package drift (re-exporting either
// the carrier struct fields or the publicValue marker interface).
//
// Detector logic and Check* functions live in errcode_invariants.go
// (non-test) so they can be compiled by external Cell repositories via
// StandardCellRules / RunStandardCellRules. Test* functions here call the
// same Check* — single source, no parallel rule body.
//
// ERRCODE-PREFIX-OWNERSHIP-01 and ERRCODE-CARVEOUT-ADR-CONSISTENCY-01 are
// intentionally NOT registered in StandardCellRules: they are bound to
// GoCell's own prefix registry / ADR, making them vacuous for external modules.
// They remain in this file.

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// INVARIANT: ERRCODE-KIND-LITERAL-01
//
// TestErrcodeLiteralConstructionBanned seals the Kind-based error model:
// callers outside pkg/errcode must use errcode.New/Wrap so every error chooses
// a transport Kind explicitly.
//
// Carve-outs are function-level only (see errcodeKindLiteralCarveOuts).
// File-level skips are intentionally removed — any new errcode.Error{} literal
// outside a carved function, even in the same file, is a violation.
func TestErrcodeLiteralConstructionBanned(t *testing.T) {
	diags := CheckErrcodeKindLiteralBanned(t, ConfigForExternalCell{
		BuildTags: FlatNonDefaultTags(),
	})
	Report(t, ruleErrcodeKindLiteral01, diags)
}

// INVARIANT: ERRCODE-CARVEOUT-ADR-CONSISTENCY-01
//
// TestErrcodeCarveOutADRConsistency enforces strict equality between the
// code-side errcodeKindLiteralCarveOuts map and the CARVEOUT-REGISTRY table
// in docs/architecture/202605121800-adr-archtest-carveout-narrow.md.
//
// Parser contract: locate <!-- CARVEOUT-REGISTRY:BEGIN --> and
// <!-- CARVEOUT-REGISTRY:END --> anchors; between them skip the header row
// (| Rule | File | Function |...) and the |---|...| separator row; for each
// remaining |-delimited row, trim-space col 2 (File) and col 3 (Function).
//
// Failure message lists code-only and ADR-only entries for readability.
func TestErrcodeCarveOutADRConsistency(t *testing.T) {
	root := findModuleRoot(t)
	adrPath := filepath.Join(root, "docs", "architecture", "202605121800-adr-archtest-carveout-narrow.md")

	adrSet, err := parseCarveOutADRRegistry(adrPath)
	require.NoError(t, err, "parse carve-out ADR registry")

	// Build code-side set.
	codeSet := make(map[carveOut]struct{}, len(errcodeKindLiteralCarveOuts))
	for k := range errcodeKindLiteralCarveOuts {
		codeSet[k] = struct{}{}
	}

	// Find ADR-only entries.
	var adrOnly []string
	for k := range adrSet {
		if _, ok := codeSet[k]; !ok {
			adrOnly = append(adrOnly, fmt.Sprintf("  ADR has {%s::%s} but code map does not", k.rel, k.fn))
		}
	}
	sort.Strings(adrOnly)

	// Find code-only entries.
	var codeOnly []string
	for k := range codeSet {
		if _, ok := adrSet[k]; !ok {
			codeOnly = append(codeOnly, fmt.Sprintf("  code map has {%s::%s} but ADR does not", k.rel, k.fn))
		}
	}
	sort.Strings(codeOnly)

	var msgs []string
	msgs = append(msgs, adrOnly...)
	msgs = append(msgs, codeOnly...)
	for _, m := range msgs {
		t.Log(m)
	}
	assert.Empty(t, msgs,
		"ERRCODE-CARVEOUT-ADR-CONSISTENCY-01: errcodeKindLiteralCarveOuts and the ADR "+
			"registry table in docs/architecture/202605121800-adr-archtest-carveout-narrow.md "+
			"must be in strict equality. Update BOTH in the same PR.")
}

// parseCarveOutADRRegistry reads the ADR file at path and extracts the
// (File, Function) pairs from the CARVEOUT-REGISTRY table.
//
// It requires the <!-- CARVEOUT-REGISTRY:BEGIN --> and <!-- CARVEOUT-REGISTRY:END -->
// markers to be present. Between them the first in-table line must be the header row
// (starting with "| Rule") and the second must be the separator row (starting with
// "|---"). Missing or mismatched structural rows are hard errors with diagnostics.
func parseCarveOutADRRegistry(path string) (map[carveOut]struct{}, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open ADR: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable

	const beginMarker = "<!-- CARVEOUT-REGISTRY:BEGIN -->"
	const endMarker = "<!-- CARVEOUT-REGISTRY:END -->"

	result := make(map[carveOut]struct{})
	inTable := false
	beginSeen := false
	endSeen := false
	headerValidated := false
	separatorValidated := false

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == beginMarker {
			inTable = true
			beginSeen = true
			continue
		}
		if strings.TrimSpace(line) == endMarker {
			endSeen = true
			break
		}
		if !inTable {
			continue
		}
		// Validate and skip header row: | Rule | File | Function | ...
		if !headerValidated {
			if !strings.HasPrefix(line, "| Rule") {
				return nil, fmt.Errorf(
					"parseCarveOutADRRegistry: %s: unexpected registry table structure at %q;"+
						" expected header row then |---| separator", path, line,
				)
			}
			headerValidated = true
			continue
		}
		// Validate and skip separator row: |---|---|...
		if !separatorValidated {
			if !strings.HasPrefix(line, "|---") {
				return nil, fmt.Errorf(
					"parseCarveOutADRRegistry: %s: unexpected registry table structure at %q;"+
						" expected header row then |---| separator", path, line,
				)
			}
			separatorValidated = true
			continue
		}
		// Parse data row: | Rule | File | Function | Reason |
		cols := strings.Split(line, "|")
		// cols[0] is empty (before first |), cols[1]=Rule, cols[2]=File, cols[3]=Function, cols[4]=Reason, cols[5]=empty
		if len(cols) < 5 {
			continue
		}
		rel := strings.TrimSpace(cols[2])
		fn := strings.TrimSpace(cols[3])
		if rel == "" || fn == "" {
			continue
		}
		result[carveOut{rel: rel, fn: fn}] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan ADR: %w", err)
	}
	if !beginSeen {
		return nil, fmt.Errorf("parseCarveOutADRRegistry: %s: CARVEOUT-REGISTRY:BEGIN marker not found", path)
	}
	if !endSeen {
		return nil, fmt.Errorf("parseCarveOutADRRegistry: %s: CARVEOUT-REGISTRY:END marker not found", path)
	}
	return result, nil
}

// TestParseCarveOutADRRegistry is a table-driven unit test for parseCarveOutADRRegistry.
// It verifies: well-formed table → entries parsed; missing BEGIN → error;
// blank line where header expected → error; missing END marker → error.
func TestParseCarveOutADRRegistry(t *testing.T) {
	wellFormed := `# ADR
<!-- CARVEOUT-REGISTRY:BEGIN -->
| Rule | File | Function | Reason |
|---|---|---|---|
| R1 | pkg/a/a.go | FuncA | reason A |
| R2 | pkg/b/b.go | FuncB | reason B |
<!-- CARVEOUT-REGISTRY:END -->
`
	tests := []struct {
		name     string
		content  string
		wantErr  string
		wantKeys []carveOut
	}{
		{
			name:    "well-formed table: 2 rows parsed",
			content: wellFormed,
			wantKeys: []carveOut{
				{rel: "pkg/a/a.go", fn: "FuncA"},
				{rel: "pkg/b/b.go", fn: "FuncB"},
			},
		},
		{
			name: "missing BEGIN marker: error",
			content: `# ADR
| Rule | File | Function | Reason |
|---|---|---|---|
<!-- CARVEOUT-REGISTRY:END -->
`,
			wantErr: "CARVEOUT-REGISTRY:BEGIN marker not found",
		},
		{
			name: "blank line where header expected: error",
			content: `# ADR
<!-- CARVEOUT-REGISTRY:BEGIN -->

| Rule | File | Function | Reason |
|---|---|---|---|
<!-- CARVEOUT-REGISTRY:END -->
`,
			wantErr: "unexpected registry table structure",
		},
		{
			name: "missing END marker: error",
			content: `# ADR
<!-- CARVEOUT-REGISTRY:BEGIN -->
| Rule | File | Function | Reason |
|---|---|---|---|
| R1 | pkg/a/a.go | FuncA | reason A |
`,
			wantErr: "CARVEOUT-REGISTRY:END marker not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Write content to a temp file so parseCarveOutADRRegistry can open it.
			tmp, err := os.CreateTemp(t.TempDir(), "adr-*.md")
			require.NoError(t, err)
			_, err = tmp.WriteString(tc.content)
			require.NoError(t, err)
			require.NoError(t, tmp.Close())

			got, err := parseCarveOutADRRegistry(tmp.Name())
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			gotSet := make(map[carveOut]struct{}, len(got))
			for k := range got {
				gotSet[k] = struct{}{}
			}
			for _, k := range tc.wantKeys {
				assert.Contains(t, gotSet, k, "expected key %v in parsed result", k)
			}
			assert.Len(t, got, len(tc.wantKeys))
		})
	}
}

// TestFindErrcodeErrorLiteralsFunctionLevel is a table-driven unit test for
// scanErrcodeErrorLiteralsInAST. It uses in-memory source strings to verify:
//   - errcode.Error{} inside a carved func is NOT reported
//   - errcode.Error{} inside a non-carved func in the same file IS reported
//   - empty carve-out map → carved-func site IS reported (proves guard is active)
//   - package-level (GenDecl) errcode.Error{} is ALWAYS reported (never carved)
//
// Blind-spot self-check: AST forms outside isErrcodeErrorType's declared range:
//
//	(a) Aliased import + construction: `import ec "...errcode"` + `ec.Error{}`
//	    — IS detected because errcodeImportNames collects aliases.
//	(b) Dot-import: `import . "...errcode"` + `Error{}`
//	    — NOT detected (errcodeImportNames skips "." alias).
//	    Reverse self-check (pure-AST, errcodeDotImported): assert no
//	    production file dot-imports errcode.
//	(c) Cross-pkg type-alias: `type MyErr = errcode.Error` in pkg B + `B.MyErr{}`
//	    — NOT detected (SelectorExpr.X.Name is B, not in errcodeNames).
//	    Reverse self-check (pure-AST, errcodeErrorAliasReexports; resolves
//	    aliased imports too): assert no production file re-exports it as alias.
func TestFindErrcodeErrorLiteralsFunctionLevel(t *testing.T) {
	// Use PlatformModulePath-derived constant to avoid bare platform literal.
	const errcodeImport = `"` + errcodeImportPath + `"`

	makeSrc := func(body string) string {
		return `package p
import ` + errcodeImport + `
` + body
	}

	tests := []struct {
		name      string
		src       string
		rel       string
		carveOuts map[carveOut]struct{}
		wantLines []int // nil or empty = expect no hits
	}{
		{
			name: "carved func: errcode.Error literal suppressed",
			src: makeSrc(`func WrapOrInfra() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/ctxcancel/ctxcancel.go",
			carveOuts: map[carveOut]struct{}{{rel: "pkg/ctxcancel/ctxcancel.go", fn: "WrapOrInfra"}: {}},
			wantLines: nil,
		},
		{
			name: "non-carved func in same file: errcode.Error literal reported",
			src: makeSrc(`func WrapOrInfra() {
	_ = errcode.Error{}
}
func OtherFunc() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/ctxcancel/ctxcancel.go",
			carveOuts: map[carveOut]struct{}{{rel: "pkg/ctxcancel/ctxcancel.go", fn: "WrapOrInfra"}: {}},
			// package p=1, import=2, WrapOrInfra decl=3, carved literal=4, }=5, OtherFunc decl=6, literal=7
			wantLines: []int{7},
		},
		{
			name: "empty carve-out map: WrapOrInfra site IS reported",
			src: makeSrc(`func WrapOrInfra() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/ctxcancel/ctxcancel.go",
			carveOuts: map[carveOut]struct{}{},
			wantLines: []int{4}, // package p=1, import=2, func decl=3, literal=4
		},
		{
			name:      "package-level errcode.Error literal: always reported (never carved)",
			src:       makeSrc(`var _ = errcode.Error{}`),
			rel:       "some/file.go",
			carveOuts: map[carveOut]struct{}{{rel: "some/file.go", fn: "someFunc"}: {}},
			wantLines: []int{3},
		},
		{
			name:      "no errcode import: no hits",
			src:       `package p; func F() {}`,
			rel:       "some/file.go",
			carveOuts: map[carveOut]struct{}{},
			wantLines: nil,
		},
		{
			// Finding F2 regression: a method whose name collides with a
			// carved package-level function must NOT inherit the carve-out.
			name: "method sharing carve-out name: NOT exempt (function-level only)",
			src: makeSrc(`func WritePublic() {
	_ = errcode.Error{}
}
type T struct{}
func (T) WritePublic() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/httputil/response.go",
			carveOuts: map[carveOut]struct{}{{rel: "pkg/httputil/response.go", fn: "WritePublic"}: {}},
			// p=1 import=2 funcWritePublic=3 carvedLit=4 }=5 typeT=6 method=7 lit=8 }=9
			wantLines: []int{8},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, tc.rel, tc.src, parser.SkipObjectResolution)
			require.NoError(t, err)

			hits := scanErrcodeErrorLiteralsInAST(fset, f, tc.rel, tc.carveOuts)
			var gotLines []int
			for _, h := range hits {
				gotLines = append(gotLines, h.line)
			}
			assert.Equal(t, tc.wantLines, gotLines)
		})
	}

	// ── Reverse self-checks for declared blind spots (b) dot-import and
	//    (c) cross-pkg type-alias re-export. Pure-AST, no string prefilter:
	//    a prefilter anchored on usage-site text (`= errcode.Error`) is blind
	//    to aliased imports (`import ec ".../errcode"; type X = ec.Error`),
	//    which is exactly the form the AST logic must catch. One walk; the
	//    AST visit is gated cheaply by errcodeImportNames inside each helper.
	t.Run("reverse-self-check: no dot-import / type-alias re-export of pkg/errcode in production", func(t *testing.T) {
		root := findModuleRoot(t)
		files, err := collectGoFiles(root)
		require.NoError(t, err)

		var dotImports, aliasReexports []string
		for _, file := range files {
			rel, _ := filepath.Rel(root, file)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "pkg/errcode/") {
				continue
			}
			if !fileroles.IsProductionCode(rel) {
				continue
			}
			data, err := os.ReadFile(filepath.Clean(file))
			require.NoError(t, err)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, data, parser.SkipObjectResolution)
			require.NoError(t, err, "parse %s", rel)
			if errcodeDotImported(f) {
				dotImports = append(dotImports, rel)
			}
			for _, name := range errcodeErrorAliasReexports(f) {
				aliasReexports = append(aliasReexports,
					fmt.Sprintf("%s: type %s = errcode.Error", rel, name))
			}
		}
		assert.Empty(t, dotImports,
			"production files must not dot-import pkg/errcode "+
				"(blind spot: dot-import escapes errcodeImportNames detection)")
		assert.Empty(t, aliasReexports,
			"production files must not re-export errcode.Error as a type alias "+
				"(blind spot: cross-pkg alias escapes pure-AST detection)")
	})
}

// TestErrcodeBlindSpotHelpers is the regression coverage for Finding F1:
// the dot-import and type-alias blind-spot detectors must work purely from
// the AST, with no source-text prefilter. The decisive case is
// "aliased import + alias re-export" — a `= errcode.Error` string prefilter
// would have skipped the AST parse entirely and missed it.
func TestErrcodeBlindSpotHelpers(t *testing.T) {
	parse := func(t *testing.T, src string) *ast.File {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
		require.NoError(t, err)
		return f
	}

	// Use PlatformModulePath-derived constant to avoid bare platform literals.
	errcodeImportLit := `"` + errcodeImportPath + `"`

	t.Run("errcodeDotImported", func(t *testing.T) {
		tests := []struct {
			name string
			src  string
			want bool
		}{
			{"dot-import detected", "package p\nimport . " + errcodeImportLit + "\n", true},
			{"normal import not flagged", "package p\nimport " + errcodeImportLit + "\n", false},
			{"aliased import not flagged", "package p\nimport ec " + errcodeImportLit + "\nvar _ = ec.Error{}\n", false},
			{"no errcode import", "package p\nimport \"fmt\"\n", false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, errcodeDotImported(parse(t, tc.src)))
			})
		}
	})

	t.Run("errcodeErrorAliasReexports", func(t *testing.T) {
		tests := []struct {
			name string
			src  string
			want []string
		}{
			{
				name: "default import name re-export detected",
				src:  "package p\nimport " + errcodeImportLit + "\ntype MyErr = errcode.Error\n",
				want: []string{"MyErr"},
			},
			{
				// Finding F1 lock: aliased import + alias re-export. A
				// `= errcode.Error` source prefilter never matches `= ec.Error`.
				name: "aliased import re-export detected",
				src:  "package p\nimport ec " + errcodeImportLit + "\ntype MyErr = ec.Error\n",
				want: []string{"MyErr"},
			},
			{
				name: "type definition (not alias) not flagged",
				src:  "package p\nimport " + errcodeImportLit + "\ntype MyErr errcode.Error\n",
				want: nil,
			},
			{
				name: "alias to other type not flagged",
				src:  "package p\nimport " + errcodeImportLit + "\ntype C = errcode.Code\n",
				want: nil,
			},
			{
				name: "no errcode import: short-circuit, nil",
				src:  "package p\ntype Error = struct{}\n",
				want: nil,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, errcodeErrorAliasReexports(parse(t, tc.src)))
			})
		}
	})
}

// INVARIANT: MESSAGE-CONST-LITERAL-01
//
// TestErrcodeMessageConstLiteral enforces MESSAGE-CONST-LITERAL-01.
//
// MESSAGE-CONST-LITERAL-01 — every call to `errcode.New(...)` and
// `errcode.Wrap(...)` in production code must pass a compile-time const
// literal as the third (`message`) argument. Runtime data (user input, IDs,
// counts, secrets) belongs in WithDetails (sealed PublicDetail via
// errcode.PublicString) or WithInternal (sealed InternalDetail, server-side
// only). The PII-safe default is enforced statically here so regression
// cannot reintroduce `fmt.Sprintf` / string-concatenation messages that
// leak runtime context onto the wire.
//
// ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md
func TestErrcodeMessageConstLiteral(t *testing.T) {
	diags := CheckErrcodeMessageConstLiteral(t, ConfigForExternalCell{
		BuildTags: FlatNonDefaultTags(),
	})
	Report(t, ruleMessageConstLiteral01, diags)
}

// INVARIANT: ERROR-FIRST-API-01
//
// TestErrorFirstAPI01 walks the enforced file list and reports panic() calls
// inside error-less function declarations: in the explicitly enrolled files
// (PR-MODE-6 scope), exported and unexported function declarations whose
// return signature does NOT include an error MUST NOT contain a `panic(...)`
// call in the function body.
//
// Companion invariant ERROR-FIRST-TYPED-NIL-01 (asserted by
// TestErrorFirstTypedNil01 below) requires error-returning New* constructors
// in the enrolled file scope to nil-guard each nil-able dependency parameter
// at construction time. Interface params must be guarded with
// validation.IsNilInterface(p) (typed-nil defeat); pointer / map / chan /
// func params may use p == nil.
func TestErrorFirstAPI01(t *testing.T) {
	diags := CheckErrorFirstAPI01(t, ConfigForExternalCell{
		BuildTags: FlatNonDefaultTags(),
	})
	Report(t, ruleErrorFirstAPI01, diags)
}

// INVARIANT: ERROR-FIRST-TYPED-NIL-01
//
// TestErrorFirstTypedNil01 verifies error-returning New* constructors in the
// enrolled file scope nil-guard each nil-able dependency parameter at
// construction time (see ERROR-FIRST-API-01 for the companion panic-free
// rule).
func TestErrorFirstTypedNil01(t *testing.T) {
	diags := CheckErrorFirstTypedNil01(t, ConfigForExternalCell{
		BuildTags: FlatNonDefaultTags(),
	})
	Report(t, ruleErrorFirstTypedNil01, diags)
}

// TestErrorFirstTypedNilScannerFixtures verifies the typed-nil guard detector
// via real fixture modules (Hard upgrade from inline-source table).
//
// Each subdirectory under testdata/errorfirsttypednilfixture/ is a standalone
// Go module. Each fixture dir owns a diag.golden capturing the rule's real
// output (Rel:Line: Message). *_passes cases have an empty golden; *_violates
// cases have one or more diagnostic lines. Line numbers live in the golden,
// never in this table. See ADR
// docs/architecture/202605181200-adr-archtest-fixture-diagnostic-golden.md.
func TestErrorFirstTypedNilScannerFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/errorfirsttypednilfixture"

	// RED (*_violates) and GREEN (*_passes) cases share the same loop body;
	// distinction is captured entirely in the golden file.
	dirs := []string{
		"constructor_interface_without_isnil_violates",
		"constructor_interface_with_isnil_passes",
		"optional_interface_with_isnil_passes",
		"non_error_constructor_passes",
		"non_constructor_function_passes",
		"isnil_result_discarded_violates",
		"isnil_inside_non_if_call_violates",
		"if_cond_no_return_violates",
		"then_in_goroutine_violates",
		"and_compound_violates",
		"pointer_param_nil_guard_passes",
		"or_compound_isnil_passes",
		"map_param_nil_guard_passes",
		"chan_param_nil_guard_passes",
		"func_param_nil_guard_passes",
		"slice_param_passes",
		"then_in_defer_violates",
		"aliased_validation_violates",
		"unnamed_param_passes",
		"blank_param_passes",
		"constructor_validate_required_delegation_passes",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := base + "/" + dir
			diags := Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: true}, []string{"./..."}),
				func(p *Pass) []Diagnostic {
					var out []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)
						out = append(out, scanTypedNilGuardsInFile(p.Fset, p.TypesInfo, file, rel)...)
					}
					return out
				})

			goldenPath := filepath.Join(base, dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}

// INVARIANT: EXPORTED-ERROR-NEW-01
//
// TestExportedErrorNew enforces EXPORTED-ERROR-NEW-01 by walking every
// production-code file (per fileroles.IsProductionCode) outside the
// pkg/errcode/ allow-list and flagging package-scope exported sentinel
// vars whose initializer is `errors.New(...)`.
//
// EXPORTED-ERROR-NEW-01 — invariant-driven gate.
//
// Invariant: In all production-shippable .go files outside pkg/errcode/,
// no top-level (package-scope) `var` declaration may bind an exported
// identifier matching the sentinel naming convention `^Err[A-Z]\w*$` to
// an `errors.New(...)` call expression. Use pkg/errcode.New(code, message)
// so the sentinel participates in the wire-protocol error code taxonomy
// and HTTP status mapping (CLAUDE.md: 禁止 errors.New 对外暴露).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G2
func TestExportedErrorNew(t *testing.T) {
	diags := CheckExportedErrorNew(t, ConfigForExternalCell{
		BuildTags: FlatNonDefaultTags(),
	})
	Report(t, ruleExportedErrorNew01, diags)
}

// ─── details_sealed_field_frozen ────────────────────────────────────────────
//
// INVARIANT: DETAILS-SEALED-FIELD-FROZEN-01
//
// errcode.PublicDetail / errcode.InternalDetail are sealed-construction
// carriers (ADR docs/architecture/202605051730-adr-errcode-message-pii-safety.md
// §Amendment 2026-05-27). The construction Hard claim requires two
// invariants that this archtest locks via reflect:
//
//  1. Field layout: both structs MUST have exactly {key, value}, both
//     unexported. An exported field re-opens the literal-construction
//     bypass (`errcode.PublicDetail{Key:..., Value:...}` becomes valid
//     from outside the package), collapsing the Hard rating.
//  2. publicValue typed-marker: errcode.PublicDetail.value MUST be the
//     sealed interface (named "publicValue", unexported method publicValue()).
//     A widening to `any` (or a renamed-but-method-exporting variant)
//     would let callers route wire-unsafe types (chan, func, NaN/Inf
//     floats, maps, structs, pointers) into Error.Details, silently
//     downgrading 4xx → 500 at json.Marshal time.
//
// InternalDetail.value is intentionally `any` (server-only channel; ADR
// amendment §"Three-layer table revision"); the test asserts that
// deliberate asymmetry so it cannot drift silently.
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Hard 范本目录
// "sealed construction"):
//   - Downstream Hard: typed Public* constructors are the only callsites
//     that can produce a non-zero publicValue (the marker method is
//     unexported, so no other package can implement it). Wire-unsafe
//     value types are inexpressible at compile time.
//   - Upstream Hard: PublicDetail.key / PublicDetail.value are unexported,
//     so outside-package struct-literal construction is a Go compile
//     error. errcode.Error.Details ([]PublicDetail) remains exported and
//     can be mutated by outside code, but only via PublicDetail values
//     produced from the typed constructors; the wire-side 5xx Details-
//     strip invariant (Error.MarshalJSON → project() → []PublicDetail{})
//     is the defense for runtime data leakage regardless of append source.
//
// Blind spots (reverse self-check below):
//   - The lock keys on field NAMES "key"/"value"; a rename surfaces as
//     a test error (visible), not a silent pass.
//   - The publicValue type identity is checked by name and by the
//     presence of an unexported method "publicValue" — an aliased type
//     declared inside pkg/errcode with the same method set would pass,
//     but it would still be sealed (the marker is package-local).
//   - Outside-package aliasing of PublicDetail (`type Foo = errcode.PublicDetail`
//     in another package) does not re-open construction: aliases preserve
//     field visibility, so unexported fields stay unconstructable.
//
// Reference: SUBSCRIBERS-DERIVED-FIELD-FROZEN-01 (same reflect lock pattern),
// OUTBOX-HANDLERESULT-FIELDS-FROZEN-01 (sibling envelope freeze).
func TestDetailsSealedFieldFrozen01(t *testing.T) {
	diags := CheckDetailsSealedFieldFrozen01(t, ConfigForExternalCell{
		BuildTags: FlatNonDefaultTags(),
	})
	Report(t, ruleDetailsSealedFieldFrozen, diags)
}

// TestDetailsSealedFieldFrozen01_ScannerFires proves the field-shape
// assertion has teeth (reverse self-check): a synthetic struct that
// violates each axis must produce a non-empty result from
// checkSealedKeyValueShape. Without this, a refactor that lowercases
// the helper's guard conditions could silently disable the lock.
//
// Synthetic types are built via reflect.StructOf so the linter does not
// see "unused" field declarations on probe-only structs.
func TestDetailsSealedFieldFrozen01_ScannerFires(t *testing.T) {
	t.Parallel()

	stringType := reflect.TypeOf("")
	anyType := reflect.TypeOf((*any)(nil)).Elem()

	mkField := func(name string, typ reflect.Type, exported bool) reflect.StructField {
		f := reflect.StructField{Name: name, Type: typ}
		if !exported {
			// Use PlatformModulePath-derived path rather than a bare literal.
			f.PkgPath = PlatformModulePath + "/tools/archtest"
		}
		return f
	}

	cases := []struct {
		name   string
		fields []reflect.StructField
		want   bool // true = scanner must report violation
	}{
		{
			name: "extraField", // wrong field count
			fields: []reflect.StructField{
				mkField("key", stringType, false),
				mkField("value", anyType, false),
				mkField("Extra", stringType, true),
			},
			want: true,
		},
		{
			name: "exportedKey", // outside-package literal construction becomes possible
			fields: []reflect.StructField{
				mkField("Key", stringType, true),
				mkField("value", anyType, false),
			},
			want: true,
		},
		{
			name: "renamedKey",
			fields: []reflect.StructField{
				mkField("ident", stringType, false),
				mkField("value", anyType, false),
			},
			want: true,
		},
		{
			name: "goodShape", // canonical {key, value} unexported — must pass
			fields: []reflect.StructField{
				mkField("key", stringType, false),
				mkField("value", anyType, false),
			},
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := checkSealedKeyValueShape(tc.name, reflect.StructOf(tc.fields))
			if tc.want && len(got) == 0 {
				t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: scanner did not fire on %s", tc.name)
			}
			if !tc.want && len(got) != 0 {
				t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: scanner false-positive on %s: %v", tc.name, got)
			}
		})
	}

	// Keep the time import live for parity with details.go imports.
	_ = time.Second
}

// ─── errcode_prefix_ownership ────────────────────────────────────────────────

// INVARIANT: ERRCODE-PREFIX-OWNERSHIP-01
//
// TestErrcodePrefixOwnership01 enforces ERRCODE-PREFIX-OWNERSHIP-01.
//
// ERRCODE-PREFIX-OWNERSHIP-01 — the errcode prefix registry is closed:
// every production Code minted by errcode.New / errcode.Wrap and every
// exported package-scope Code sentinel must have a registered prefix entry
// in pkg/errcode.RegisteredPrefixes().
//
// Two scan targets share one classifier, classifyCodeExpr (const /
// runtime-assembled / skip):
//
//	A. Mint callsites: every code-bearing helper in codeGatedCallees
//	   (errcode.New/Wrap/WrapInfra, httputil.WritePublic, ctxcancel.WrapOrInfra)
//	   — extract the code arg at the helper's codeArgIndex.
//	B. Sentinel decls: every exported errcode.Code-typed Err* sentinel at
//	   package scope. In typed mode the declared type is confirmed via go/types
//	   (isErrcodeCodeSentinel), which excludes Err*-named non-Code sentinels
//	   (var ErrX = errcode.New(...) is *errcode.Error; errors.New(...) is error).
//
// Both targets resolve the value with EvaluateConstString — including const
// SelectorExpr / Ident forms (e.g. var ErrX errcode.Code = somepkg.Const), not
// just string BasicLits. A const value whose prefix is unregistered is reported;
// a runtime-assembled value (errcode.Code("ERR_"+x) or other type-conversion /
// concatenation) is a HARD FAIL closing the closed-set escape hatch.
// Parse/compare-side errcode.Code(x) conversions are NOT mint sites or sentinels
// and are untouched.
//
// §Residual (rating: Medium — gh #1508). This rule is archtest-bound, not a
// type-system seal: errcode.Code is a wire string (JSON-serialized, parsed from
// responses, compared in tests), so sealing it into a closed-constructor type
// would break the parse side and is rejected (see ADR §备选). Hard is therefore
// unreachable; the whole rule is Medium. After the gh #1508 fix the SOLE residual
// is the forwarding-launder gap: a bare non-const Code variable/parameter
// reference (classifyCodeExpr → codeArgSkip) at a mint site or sentinel value is
// skipped, because resolving it needs data-flow / taint tracing that archtest
// does not do and that would false-positive on legitimate parse/compare-side
// conversions. Only deliberate construction triggers it, not accidental drift.
// This is the same permanent Go-language ceiling family as
// SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893) / outbox
// principal-write (#1282). The earlier "non-literal sentinel" residual is CLOSED:
// Target B now resolves const SelectorExpr/Ident values via type info (see
// TestErrcodePrefixOwnership01_SentinelConstEval).
//
// After scanning, a canary coverage anchor asserts that ERR_AUTH_FORBIDDEN and
// ERR_INTERNAL were actually observed; if absent the scan loaded nothing and
// a false-green from an empty scope is rejected.
//
// ref: docs/architecture/202606031200-1091-adr-errcode-prefix-ownership-registry.md
// Issue #1091, #1508.
func TestErrcodePrefixOwnership01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsWithSatellites(root)

	visited := map[string]bool{}
	canaryObserved := map[string]bool{}
	const canaryA = "ERR_AUTH_FORBIDDEN"
	const canaryB = "ERR_INTERNAL"

	diags := Run(t, Typed(
		TypedOpts{Tests: false, Tags: []string{"e2e", "integration", "pg"}},
		patterns,
	),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				abs := p.Abs(file)
				if visited[abs] {
					continue
				}
				visited[abs] = true

				rel := p.Rel(file)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				// Skip archtest testdata fixtures (intentional violations).
				if strings.HasPrefix(rel, errcodeMessageTestdataAllowlist) {
					continue
				}
				// pkg/errcode itself declares all platform sentinels. Scan it for
				// Target B (sentinel decls) — this closes the F3 gap where a new
				// unregistered sentinel added directly to errcode.go escaped the
				// closed set entirely. Disable Target A there: same-package New/Wrap
				// calls are bare idents, not qualified selectors, so they never
				// match the code gate.
				scanTargetA := !strings.HasPrefix(rel, errcodeMessageAllowlist)

				fileDiags, seen := scanErrcodePrefixOwnershipDiags(p.Fset, file, rel, p.TypesInfo, scanTargetA)
				out = append(out, fileDiags...)
				for _, c := range seen {
					canaryObserved[c] = true
				}
			}
			return out
		})

	// Canary coverage anchor: if neither canary code was observed, the scan
	// loaded nothing (scope regression) and we must reject the false-green.
	if !canaryObserved[canaryA] || !canaryObserved[canaryB] {
		t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01: canary anchor failed — "+
			"production scan did not observe codes %q (seen=%v) and %q (seen=%v). "+
			"This means the scan loaded nothing or the scope regressed; "+
			"check prodscan.PatternsWithSatellites and fileroles.IsProductionCode.",
			canaryA, canaryObserved[canaryA], canaryB, canaryObserved[canaryB])
	}

	Report(t, "ERRCODE-PREFIX-OWNERSHIP-01", diags)
}

// TestErrcodePrefixOwnership01_ScannerFires proves the ERRCODE-PREFIX-OWNERSHIP-01
// scanner has teeth (reverse self-check): a fixture file with both a non-const
// mint and an unregistered prefix literal must produce diagnostics for both.
//
// The fixture is loaded in AST-only mode (no packages.Load, no module resolution),
// mirroring the pattern of TestDetailsSealedFieldFrozen01_ScannerFires.
//
// Fixture file:
//
//	tools/archtest/testdata/errcode_prefix_ownership_fixtures/fixture.go
//
// Expected diagnostics: one for the non-const mint (hard fail) and one for
// the unregistered-prefix string literal mint.
func TestErrcodePrefixOwnership01_ScannerFires(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "errcode_prefix_ownership_fixtures")

	var allDiags []Diagnostic
	Run(t, AST(DirsScope(fixtureDir, []string{"."})), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			// nil TypesInfo = AST-only mode (fixture scan); scan both targets.
			diags, _ := scanErrcodePrefixOwnershipDiags(p.Fset, file, rel, nil, true)
			allDiags = append(allDiags, diags...)
		}
		return nil
	})

	// Must have reported at least 2 diagnostics:
	// 1. Non-const mint hard fail.
	// 2. Unregistered prefix literal.
	if len(allDiags) < 2 {
		t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01_ScannerFires: expected ≥2 diagnostics from fixture, got %d: %v",
			len(allDiags), allDiags)
		return
	}

	foundNonConst := false
	foundUnregistered := false
	for _, d := range allDiags {
		if strings.Contains(d.Message, "runtime-assembled Code value") {
			foundNonConst = true
		}
		if strings.Contains(d.Message, "prefix not registered") {
			foundUnregistered = true
		}
	}
	if !foundNonConst {
		t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01_ScannerFires: no runtime-assembled-mint diagnostic found in: %v", allDiags)
	}
	if !foundUnregistered {
		t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01_ScannerFires: no unregistered-prefix diagnostic found in: %v", allDiags)
	}

	// Each non-New/Wrap code-bearing helper must be exercised by the scan
	// (regression guard for F4: WrapInfra/WritePublic/WrapOrInfra were missing
	// from codeGatedCallees). The unregistered-prefix diagnostic names the
	// helper's displayName, so assert each appears.
	for _, want := range []string{"errcode.WrapInfra", "httputil.WritePublic", "ctxcancel.WrapOrInfra"} {
		hit := false
		for _, d := range allDiags {
			if strings.Contains(d.Message, want) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01_ScannerFires: helper %q not exercised by code gate; got: %v", want, allDiags)
		}
	}
}

// TestErrcodePrefixOwnership01_SentinelConstEval proves Target B resolves
// non-literal (const SelectorExpr / Ident) sentinel values via typed const
// evaluation, closing residual #2 (gh #1508). Before the typed-const-eval fix,
// scanSentinelValueSpec matched only *ast.BasicLit string values and silently
// skipped const SelectorExpr / Ident forms, letting an unregistered prefix be
// laundered into an exported errcode.Code sentinel.
//
// The fixture is a standalone typed module (its own go.mod) — const
// SelectorExpr / Ident resolution needs go/types (EvaluateConstString), which
// is only available under StandaloneModule loading, never AST-only mode.
//
// Fixture module:
//
//	tools/archtest/testdata/errcode_prefix_ownership_selector_fixtures/
//
// Expected: the three errcode.Code-typed non-literal sentinels are flagged —
// untyped const SelectorExpr (codes.Unregistered → ERR_SELECTORBOGUS_NOPE),
// typed errcode.Code const SelectorExpr (codes.UnregisteredTyped →
// ERR_TYPEDSELECTOR_NOPE), and same-package const Ident (localUnregistered →
// ERR_IDENTBOGUS_NOPE). NOT flagged: the registered control (ERR_INTERNAL), and
// the two Err*-named NON-Code sentinels (ErrIgnoredStdlib is error;
// ErrIgnoredErrcodeNew is *errcode.Error) that the isErrcodeCodeSentinel type
// gate must exclude. Together these prove the scan is type-selective and
// value-resolving, not a name-only blanket flag (anti-vacuity).
func TestErrcodePrefixOwnership01_SentinelConstEval(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "errcode_prefix_ownership_selector_fixtures")

	var allDiags []Diagnostic
	Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			for _, file := range p.Files {
				rel := p.Rel(file)
				diags, _ := scanErrcodePrefixOwnershipDiags(p.Fset, file, rel, p.TypesInfo, true)
				allDiags = append(allDiags, diags...)
			}
			return nil
		})

	// The three non-literal-value errcode.Code sentinels must be flagged
	// (residual #2 closed): untyped const SelectorExpr, typed Code const
	// SelectorExpr, and same-package const Ident.
	for _, want := range []string{"ERR_SELECTORBOGUS_NOPE", "ERR_TYPEDSELECTOR_NOPE", "ERR_IDENTBOGUS_NOPE"} {
		found := false
		for _, d := range allDiags {
			if strings.Contains(d.Message, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01_SentinelConstEval: non-literal sentinel %q "+
				"not flagged (residual #2 / gh #1508 escape); got %d diag(s): %v", want, len(allDiags), allDiags)
		}
	}

	// Anti-vacuity: neither the registered control nor the two Err*-named
	// non-Code sentinels may be flagged — proving the typed scan is
	// type-selective rather than name-only.
	for _, unwanted := range []string{"ERR_INTERNAL", "ERR_STDLIBIGNORED_NOPE"} {
		for _, d := range allDiags {
			if strings.Contains(d.Message, unwanted) {
				t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01_SentinelConstEval: control %q wrongly "+
					"flagged (false positive — type gate or ownership leak): %v", unwanted, d)
			}
		}
	}
	if len(allDiags) != 3 {
		t.Errorf("ERRCODE-PREFIX-OWNERSHIP-01_SentinelConstEval: expected exactly 3 diagnostics "+
			"(the three unregistered non-literal errcode.Code sentinels), got %d: %v", len(allDiags), allDiags)
	}
}

// TestErrcodeProductionScansUseSatelliteScope locks #2148: the two errcode
// production scans — runErrcodeTypedScan (shared by MESSAGE-CONST-LITERAL-01,
// EXPORTED-ERROR-NEW-01, …) and TestErrcodePrefixOwnership01
// (ERRCODE-PREFIX-OWNERSHIP-01) — must resolve their scan patterns via
// prodscan.PatternsWithSatellites, the satellite-inclusive scope, never
// prodscan.PatternsExtended, which drops the go.work satellite parents (cmd/,
// adapters/, examples/) and the top-level module roots (corecells/,
// cellmodules/). errcode intentionally stays on prodscan patterns (not
// Production()) because it must also cover tools/ and tests/ production files,
// which Production() excludes.
//
// AI-robust: Medium (type-aware AST scan of the two scan-entry functions; a
// regression back to PatternsExtended re-vacates satellite coverage and fails
// here). Mirrors TestClockChecksDoNotUseProdscanPatternsExtended. Satellite
// inclusion is a per-gate opt-in by name (see prodscan.PatternsWithSatellites
// godoc) — this guard is scoped to errcode's two functions, not a global ban.
//
// Residual (Soft, accepted): pure AST name-match on `prodscan.PatternsWithSatellites`
// — an import alias (e.g. `ps "…/prodscan"; ps.PatternsWithSatellites`) would bypass
// it. Kept Soft because both files' import blocks are trivially audited, and the
// actual satellite coverage is independently proven by the anti-vacuity test
// TestErrcodeScanScopeIncludesSatellites.
func TestErrcodeProductionScansUseSatelliteScope(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	targets := []struct{ file, fn string }{
		{"errcode_invariants.go", "runErrcodeTypedScan"},
		{"errcode_invariants_test.go", "TestErrcodePrefixOwnership01"},
	}
	for _, tg := range targets {
		path := filepath.Join(root, "tools", "archtest", tg.file)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", tg.file, err)
		}
		var found, sawSatellites bool
		EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Name == nil || fn.Body == nil || fn.Name.Name != tg.fn {
				return
			}
			found = true
			EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "prodscan" {
					return
				}
				switch sel.Sel.Name {
				case "PatternsWithSatellites":
					sawSatellites = true
				case "PatternsExtended":
					t.Errorf("%s.%s must not call prodscan.PatternsExtended; it drops go.work "+
						"satellite modules — use PatternsWithSatellites (#2148)", tg.file, tg.fn)
				}
			})
		})
		if !found {
			t.Errorf("%s: function %s not found (renamed?); update "+
				"TestErrcodeProductionScansUseSatelliteScope", tg.file, tg.fn)
		}
		if !sawSatellites {
			t.Errorf("%s.%s must call prodscan.PatternsWithSatellites so errcode scans "+
				"go.work satellite modules (#2148)", tg.file, tg.fn)
		}
	}
}

// TestErrcodeScanScopeIncludesSatellites is the self-contained anti-vacuity
// companion to TestErrcodeProductionScansUseSatelliteScope (#2149 review F3):
// the AST guard proves errcode calls prodscan.PatternsWithSatellites; this proves
// that scope actually visits satellite-module production files, so errcode
// satellite coverage (#2148) is non-vacuous. Mirrors
// TestPanicRegisteredScopeIncludesSatellites / TestClockWorkspaceScopeIncludesSatellites.
func TestErrcodeScanScopeIncludesSatellites(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	assertScopeVisits(t, "errcode (prodscan.PatternsWithSatellites)",
		Typed(TypedOpts{Tests: false}, prodscan.PatternsWithSatellites(root)),
		"cmd/gocell/main.go", "cmd/corebundle/main.go")
}

// ─── import anchors ───────────────────────────────────────────────────────────

// These blank-identifier references keep frequently-used imports live so that
// go tooling (goimports, vet) does not remove them between edits.
var (
	_ = prodscan.PatternsWithSatellites
	_ = fileroles.IsProductionCode
)
