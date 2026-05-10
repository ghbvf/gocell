// invariants:
//   - INVARIANT: CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01
//
// CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 — exported With* Option functions
// declared at the cell-package root (cells/<x>/*.go +
// examples/<demo>/cells/<x>/*.go) MUST NOT accept raw infra types
// (persistence.TxRunner / outbox.Publisher / outbox.Writer) as parameters.
// Composition roots wrap raw infra into sealed marker types
// (persistence.CellTxManager / outbox.CellPublisher / outbox.CellWriter)
// before calling cell With* Options.
//
// AI-rebust 评级：Medium (archtest type-aware via typeseval.SharedResolver
// + types.Unalias). The kernel sealed marker is the AI-HARD primary
// defense — it prevents writing a cell.go field typed `persistence.TxRunner`
// and routing assignment via WrapForCell from a non-allowlisted location.
// This archtest covers the orthogonal attack surface: sealed marker does
// not stop a cell author from declaring a public Option that *accepts* raw
// types and then passes them straight to internal services. A real
// double-defense: Hard (sealed) for fields/wiring + Medium (archtest) for
// the public API surface.
//
// types.Unalias is mandatory because Go 1.23+ materializes go/types.Alias
// by default; raw type assertion alone (`*types.Named`) would resolve a
// `type LocalTx = persistence.TxRunner` parameter to the local alias name
// and miss the violation.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md
// ref: ADR 202605101800 §D6 — predecessor archtest scanner retired; sealed marker (Hard) + this Medium guard combination replaces it.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// rawPublicOptionForbidden is the closed set of raw infra types that public
// With* Options on cells/<x>/*.go + examples/<demo>/cells/<x>/*.go must NOT
// accept. Adding a new type is permanent (AI-HARD): every existing exported
// With* re-evaluates against the new entry.
var rawPublicOptionForbidden = map[string]bool{
	"github.com/ghbvf/gocell/kernel/persistence.TxRunner": true,
	"github.com/ghbvf/gocell/kernel/outbox.Publisher":     true,
	"github.com/ghbvf/gocell/kernel/outbox.Writer":        true,
}

type rawPublicOptionViolation struct {
	File       string
	Line       int
	FuncName   string
	ParamType  string
	ParamIndex int
}

func (v rawPublicOptionViolation) String() string {
	return fmt.Sprintf("%s:%d func %s param[%d] = %s", v.File, v.Line, v.FuncName, v.ParamIndex, v.ParamType)
}

// isCellPackageRootFile returns true for files at the cell package root —
// the exact scope where public With* Options live:
//
//   - cells/<x>/<file>.go  (parts == 3, parts[0]=="cells")
//   - examples/<demo>/cells/<x>/<file>.go  (parts == 5, parts[0]=="examples", parts[2]=="cells")
//
// Excludes _test.go and _gen.go (codegen output is not author-controlled
// and is governed by the codegen contract instead).
//
// Sub-packages like cells/<x>/internal/, cells/<x>/slices/, cells/<x>/postgres/
// are NOT in scope: those are cell-internal layers whose With* (if any)
// accept raw types because they are below the cell boundary — the sealed
// marker boundary is exactly the public cell.go API.
func isCellPackageRootFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	if strings.HasSuffix(rel, "_gen.go") {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) == 3 && parts[0] == "cells" {
		return true
	}
	if len(parts) == 5 && parts[0] == "examples" && parts[2] == "cells" {
		return true
	}
	return false
}

// publicOptionParamCanonical resolves a parameter type expression to its
// canonical "<pkg-path>.<type-name>" string, applying:
//
//  1. Pointer indirection strip (`*T` → `T`)
//  2. types.Unalias (Go 1.23+ alias materialization bypass)
//  3. *types.Named extraction
//
// Returns "" for non-Named types (struct literals, interfaces, etc.) —
// those cannot match the forbidden set.
func publicOptionParamCanonical(info *types.Info, expr ast.Expr) string {
	if info == nil {
		return ""
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return ""
	}
	t := tv.Type
	for {
		ptr, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = ptr.Elem()
	}
	t = types.Unalias(t)
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj.Pkg() == nil {
		return obj.Name()
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

// scanPackagesForRawPublicOption is the inner walker used by both the
// real-repo scan and the fixture detection test. When restrictToCellRoots
// is true, only files matching isCellPackageRootFile are scanned (the
// real-repo invariant); when false, all files in supplied packages are
// scanned (the fixture detection test, where the fixture lives outside
// real cell paths).
func scanPackagesForRawPublicOption(root string, pkgs []*packages.Package, restrictToCellRoots bool) []rawPublicOptionViolation {
	var out []rawPublicOptionViolation
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			rel, err := filepath.Rel(root, absPath)
			if err != nil {
				continue
			}
			relSlash := filepath.ToSlash(rel)
			if restrictToCellRoots && !isCellPackageRootFile(relSlash) {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !fn.Name.IsExported() ||
					!strings.HasPrefix(fn.Name.Name, "With") {
					continue
				}
				if fn.Type.Params == nil {
					continue
				}
				idx := 0
				for _, field := range fn.Type.Params.List {
					canonical := publicOptionParamCanonical(pkg.TypesInfo, field.Type)
					count := len(field.Names)
					if count == 0 {
						count = 1
					}
					for k := 0; k < count; k++ {
						if rawPublicOptionForbidden[canonical] {
							out = append(out, rawPublicOptionViolation{
								File:       relSlash,
								Line:       pkg.Fset.Position(field.Pos()).Line,
								FuncName:   fn.Name.Name,
								ParamType:  canonical,
								ParamIndex: idx,
							})
						}
						idx++
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].FuncName < out[j].FuncName
	})
	return out
}

// INVARIANT: CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01
//
// TestCellRawInfraPublicOptionParam01_RealRepoClean verifies that no
// production cell-package root file declares an exported With* Option that
// accepts a forbidden raw infra type as a parameter. Detection capability
// is verified by the sibling ScannerCatchesViolation test.
func TestCellRawInfraPublicOptionParam01_RealRepoClean(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, false, nil, "./...")
	require.NoError(t, err)

	violations := scanPackagesForRawPublicOption(root, resolver.Packages(), true)
	for _, v := range violations {
		t.Errorf("CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01: %s:%d func %s(...) param[%d] type=%s — "+
			"public Option must accept sealed marker (persistence.CellTxManager / outbox.Cell{Publisher,Writer}) "+
			"instead of raw infra; composition roots wrap via persistence.WrapForCell / outbox.Wrap{Publisher,Writer}ForCell.",
			v.File, v.Line, v.FuncName, v.ParamIndex, v.ParamType)
	}
}

// INVARIANT: CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01
//
// TestCellRawInfraPublicOptionParam01_ScannerCatchesViolation loads the
// build-tag-gated rawparamfixture package and asserts the scanner reports
// every forbidden-param case (3 raw types + 1 type-alias bypass = 4
// violations across 4 With* funcs).
//
// Per ai-collab.md §"real source AST capture (AI 难造假)": fixture is a
// real Go package loaded via packages.Load with the archtest_fixture
// build tag. Bypassing this test requires modifying real source code.
func TestCellRawInfraPublicOptionParam01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, false, []string{"archtest_fixture"},
		"./tools/archtest/internal/rawparamfixture")
	require.NoError(t, err)

	violations := scanPackagesForRawPublicOption(root, resolver.Packages(), false)
	require.Len(t, violations, 4,
		"fixture must yield 4 violations: WithBadTxRunner / WithBadPublisher / "+
			"WithBadWriter / WithAliasedBadTxRunner")

	got := map[string]string{}
	for _, v := range violations {
		got[v.FuncName] = v.ParamType
	}
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", got["WithBadTxRunner"])
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/outbox.Publisher", got["WithBadPublisher"])
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/outbox.Writer", got["WithBadWriter"])
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", got["WithAliasedBadTxRunner"],
		"types.Unalias must resolve type alias to canonical raw type")
}
