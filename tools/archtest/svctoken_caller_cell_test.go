// INVARIANT: SVCTOKEN-CALLER-CELL-REQUIRED-01
//
// # SVCTOKEN-CALLER-CELL-REQUIRED-01
//
// Invariant: every call expression `auth.GenerateServiceToken(...)` must
// pass a non-empty string literal as its second argument (callerCell).
// The literal must:
//   - match metadata.CellIDPattern (^[a-z][a-z0-9]+$, no dash, ≥2 chars)
//   - be a known cell ID according to cells/ directory names OR actors.yaml
//
// This gate prevents callers from omitting the caller identity or using an
// unregistered cell name, which would defeat the purpose of 4-part service
// token caller-cell propagation.
//
// Detection: type-aware — resolved via ResolvePackageRef which
// uniformly handles SelectorExpr (path A.2 qualified `auth.GenerateServiceToken`)
// and Ident (path A.3 dot-imported bare `GenerateServiceToken` after
// `import . ".../runtime/auth"`). Closes PR445-FU-PACKAGEALIASES-TYPE-AWARE-01
// + PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01 caller migration: pre-PR-TS2
// the matcher only saw SelectorExpr-shaped call sites; dot-imported bare
// `GenerateServiceToken(...)` silently slipped through.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// ruleSvctokenCallerCellRequired01 is the archtest rule identifier; not a credential.
//
//nolint:gosec // G101 false positive: archtest rule identifier, not a credential
const ruleSvctokenCallerCellRequired01 = "SVCTOKEN-CALLER-CELL-REQUIRED-01"

// authRuntimeImportPath is the canonical import path for runtime/auth.
// Shared with role_admin_literal_test.go in the same archtest package; do
// not duplicate.
const authRuntimeImportPath = "github.com/ghbvf/gocell/runtime/auth"

// TestSVCTOKEN_CALLER_CELL_REQUIRED_01 enforces that every call to
// auth.GenerateServiceToken passes a valid cell-ID string literal as its
// second argument (callerCell).
//
// Note: this test FAILS (RED) until Wave 2 updates GenerateServiceToken to
// accept callerCell as the second parameter AND all call sites are migrated.
func TestSVCTOKEN_CALLER_CELL_REQUIRED_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	knownCells := discoverKnownCells(t, root)

	// tests=true loads the test variants of every package, so test helpers
	// (e.g. examples/ssobff/walkthrough_test.go) that call
	// GenerateServiceToken are also scanned. FlatNonDefaultTags()
	// returns the union of every tag tracked in KnownNonDefaultTags(); a
	// single RunTyped call carrying all tags simultaneously satisfies
	// every //go:build constraint at once (e.g. `//go:build integration` is
	// included because `integration` is in the set; `//go:build integration
	// && otelcollector` is included because both tags are present). This
	// closes PR445-FU finding F2 (the prior nil-tags call silently skipped
	// integration / e2e / examples_smoke files).
	//
	// Single-load avoids retaining 7 independent type graphs in
	// SharedResolver's cache (one per tag combination), which OOM'd CI
	// runners with ~7GB RAM. The flat-load is functionally equivalent for
	// per-callsite rules; downstream callers that need per-tag-set
	// disposition can iterate KnownNonDefaultTags() themselves with
	// post-load filtering by file build constraints.
	seen := map[string]struct{}{}
	var diags []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		[]string{"./runtime/...", "./cells/...", "./cmd/...", "./examples/...", "./tests/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			authOwningPackage := p.Pkg.Path() == authRuntimeImportPath
			for _, file := range p.Files {
				rel := p.Rel(file)
				if authOwningPackage && strings.HasSuffix(rel, "_test.go") {
					continue
				}
				collectGenerateServiceTokenDiagsFromFile(p, file, rel, knownCells, seen, &diags)
			}
			return nil
		})
	Report(t, ruleSvctokenCallerCellRequired01, diags)
}

// collectGenerateServiceTokenDiagsFromFile walks a single file's AST for
// GenerateServiceToken callsites and appends any diagnostics to *diags,
// deduplicating by (rel, line, message) via the seen map.
func collectGenerateServiceTokenDiagsFromFile(
	p *Pass,
	file *ast.File,
	rel string,
	knownCells map[string]bool,
	seen map[string]struct{},
	diags *[]Diagnostic,
) {
	add := func(d Diagnostic) {
		key := fmt.Sprintf("%s:%d:%s", d.Rel, d.Line, d.Message)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*diags = append(*diags, d)
	}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		path, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || path != authRuntimeImportPath || name != "GenerateServiceToken" {
			return
		}
		pos := p.Fset.Position(call.Pos())

		// The 4-part signature is GenerateServiceToken(ring, callerCell, method, path, query, ts).
		// callerCell is argument index 1 (0-based).
		if len(call.Args) < 2 {
			add(Diagnostic{
				Rel:     rel,
				Line:    pos.Line,
				Message: "auth.GenerateServiceToken called with fewer than 2 arguments — missing callerCell",
			})
			return
		}

		arg1 := call.Args[1]
		lit, isLit := arg1.(*ast.BasicLit)
		if !isLit {
			add(Diagnostic{
				Rel:     rel,
				Line:    pos.Line,
				Message: "auth.GenerateServiceToken second argument (callerCell) must be a string literal",
			})
			return
		}
		callerCell, ok := StringLitValue(lit)
		if !ok {
			add(Diagnostic{
				Rel:     rel,
				Line:    pos.Line,
				Message: "auth.GenerateServiceToken second argument (callerCell) must be a string literal",
			})
			return
		}

		if callerCell == "" {
			add(Diagnostic{
				Rel:     rel,
				Line:    pos.Line,
				Message: "auth.GenerateServiceToken callerCell must not be empty",
			})
			return
		}

		if !metadata.MatchCellID(callerCell) {
			add(Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"auth.GenerateServiceToken callerCell %q does not match metadata.CellIDPattern (%s)",
					callerCell, metadata.CellIDPattern),
			})
			return
		}

		if !knownCells[callerCell] {
			add(Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"auth.GenerateServiceToken callerCell %q is not a known cell ID"+
						" — register it in cells/ or actors.yaml", callerCell),
			})
		}
	})
}

// TestSVCTOKEN_CALLER_CELL_REQUIRED_01_BuildTaggedFilesScanned_Wave5_RED is a
// RED-step regression test (TDD per ai-collab.md) for PR445-FU finding F2.
//
// The production rule TestSVCTOKEN_CALLER_CELL_REQUIRED_01 calls
// RunTyped with FlatNonDefaultTags() — so packages.Load uses all known
// build tags and includes every file gated by `//go:build <tag>`. Two real
// callsites of auth.GenerateServiceToken are gated this way and therefore
// escape the rule without the flat-tag load:
//
//   - examples/ssobff/walkthrough_test.go  (//go:build integration)
//   - tests/integration/internal_rpc_caller_cell_test.go  (//go:build integration)
//
// Wave 5 introduces FlatNonDefaultTags() — the union of every tag tracked
// in KnownNonDefaultTags() — and the production rule loads once with all
// tags simultaneously. This test asserts the load contract directly:
// build-tagged files MUST be loaded so the rule's scan loop actually reaches
// their callsites.
//
// Wave 1 (tags=nil): integration-tagged files NOT in loaded set →
// assertion fails → RED.
//
// Wave 5 (FlatNonDefaultTags single load): integration-tagged files
// loaded → assertion passes → GREEN. The sub-test mirrors the production
// loader exactly.
//
// Single-load (vs the obvious "iterate KnownNonDefaultTags() and call
// RunTyped per tag-set") avoids retaining 7 independent type graphs in
// SharedResolver's package-cache, which OOM'd CI runners with ~7GB RAM
// before this fix.
func TestSVCTOKEN_CALLER_CELL_REQUIRED_01_BuildTaggedFilesScanned_Wave5_RED(t *testing.T) {
	t.Parallel()

	// Mirror the production rule's loader call (single flat-tag load) exactly.
	loadedFiles := map[string]bool{}
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		[]string{"./runtime/...", "./cells/...", "./cmd/...", "./examples/...", "./tests/..."},
		func(p *Pass) []Diagnostic {
			for _, file := range p.Files {
				loadedFiles[p.Rel(file)] = true
			}
			return nil
		})

	// Files known to contain auth.GenerateServiceToken callsites under
	// `//go:build integration`. Wave 5's FlatNonDefaultTags load must
	// load each.
	expectedScanned := []string{
		"examples/ssobff/walkthrough_test.go",
		"tests/integration/internal_rpc_caller_cell_test.go",
	}

	var missing []string
	for _, want := range expectedScanned {
		if !loadedFiles[want] {
			missing = append(missing, want)
		}
	}

	if len(missing) > 0 {
		t.Errorf("SVCTOKEN-CALLER-CELL-REQUIRED-01: %d build-tagged files are "+
			"silently skipped by the current tags=nil load — these contain "+
			"auth.GenerateServiceToken callsites that the rule MUST scan: %v",
			len(missing), missing)
	}
}

// discoverKnownCells returns the set of valid caller cell IDs from
// ProjectMeta.Cells (covers both top-level and examples cells via
// metadata path-pattern matching) plus actor IDs from actors.yaml.
func discoverKnownCells(t *testing.T, root string) map[string]bool {
	t.Helper()
	known := map[string]bool{}

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	for id := range project.Cells {
		if metadata.MatchCellID(id) {
			known[id] = true
		}
	}
	for _, a := range project.Actors {
		if metadata.MatchCellID(a.ID) {
			known[a.ID] = true
		}
	}
	return known
}
