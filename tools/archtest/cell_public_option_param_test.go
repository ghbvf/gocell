// invariants:
//   - INVARIANT: CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01
//
// CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 — exported With* Option functions
// declared anywhere in a cell subtree (cells/<x>/**/*.go +
// examples/<demo>/cells/<x>/**/*.go, covering cell-package root +
// internal/ + slices/<y>/ + postgres/ + mem/ ...) MUST NOT accept raw
// infra types (persistence.TxRunner / outbox.Publisher / outbox.Writer)
// as parameters. Composition roots wrap raw infra into sealed marker
// types (persistence.CellTxManager / outbox.CellPublisher / outbox.CellWriter)
// before calling any cell-subtree With* Option.
//
// Scope was extended from cell-package root only to the full cell subtree
// by ADR 202605101900 Amendment 2026-05-12 (PR #481 / PR-S7); the previous
// `isCellPackageRootFile` predicate is now `isCellSubtreeFile`.
//
// AI-rebust 评级：Medium (archtest type-aware via RunTypedProduction
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

	"github.com/ghbvf/gocell/tools/typesutil"
)

// expectedRawParamFixtureViolations is the number of CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01
// violations declared in tools/archtest/internal/rawparamfixture/cell.go.
// When adding new violation cases to the fixture, update this constant first.
const expectedRawParamFixtureViolations = 10

// rawPublicOptionForbidden is the closed set of raw infra types that public
// With* Options anywhere in a cell subtree (cells/<x>/**/*.go +
// examples/<demo>/cells/<x>/**/*.go) must NOT accept. Adding a new type is
// permanent (AI-HARD): every existing exported With* re-evaluates against
// the new entry.
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

// isCellSubtreeFile returns true for any non-codegen, non-test file inside
// a cell subtree — the full scope where sealed-marker enforcement applies:
//
//   - cells/<x>/**/*.go                  (cell-package root + internal/ +
//     slices/<y>/ + postgres/ + mem/ + ...)
//   - examples/<demo>/cells/<x>/**/*.go  (example cell subtree, same layout)
//
// Excludes _test.go and _gen.go (codegen output is governed by the codegen
// contract; tests construct fakes via persistence.WrapForCell from any
// location per CELL-RAW-INFRA-WRAPPER-LOCATION-01 allowlist).
//
// Sealed-marker boundary covers the full cell subtree (not just cell.go) —
// every exported With* Option in cells/<x>/{cell.go, slices/<y>/service.go,
// internal/.../service.go, ...} must accept `persistence.CellTxManager` /
// `outbox.Cell{Publisher,Writer}`, not raw infra. See ADR 202605101900 §D1
// (boundary extension, Amendment 2026-05-12) for the architectural rationale.
func isCellSubtreeFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	if strings.HasSuffix(rel, "_gen.go") {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 3 {
		return false
	}
	if parts[0] == "cells" {
		return true
	}
	if len(parts) >= 5 && parts[0] == "examples" && parts[2] == "cells" {
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
//  4. *types.Interface inline-embed walk — anonymous interface params
//     `func WithBad(p interface{ outbox.Publisher })` resolve to
//     *types.Interface (not *types.Named); the embedded named types are
//     walked and the first forbidden hit (or any embedded canonical) is
//     returned.
//  5. *types.Interface method-set fall-through — pure-method anonymous
//     interface params `func WithBad(tx interface{ RunInTx(...) error })`
//     have NumEmbeddeds()==0 but still implement a forbidden interface by
//     structural typing. forbiddenIfaces (lazy-loaded once per scan) lets
//     types.Implements detect the assignability bypass. nil forbiddenIfaces
//     skips fall-through (caller-supplied; tests may pass nil to exercise
//     only the embed path).
//
// Returns "" for non-matching types (struct literals, plain interfaces with
// no forbidden relation, etc.). Callers compare against
// rawPublicOptionForbidden to decide whether to record a violation.
func publicOptionParamCanonical(info *types.Info, expr ast.Expr, forbiddenIfaces map[string]*types.Interface) string {
	if info == nil {
		return ""
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return ""
	}
	return canonicalFromType(tv.Type, forbiddenIfaces)
}

// canonicalFromType is the recursive core of publicOptionParamCanonical
// — separated so it can be called both with the parameter's tv.Type and
// with each embedded type of an anonymous interface.
func canonicalFromType(t types.Type, forbiddenIfaces map[string]*types.Interface) string {
	for {
		ptr, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = ptr.Elem()
	}
	t = types.Unalias(t)
	// TypeParam: generic function parameter constrained to a forbidden type.
	// e.g. func WithGenericTx[T persistence.TxRunner](tx T) — tv.Type is
	// *types.TypeParam; Constraint() resolves to the bound interface/type.
	// Without this case, a type-parameter-constrained bypass silently passes.
	if tp, ok := t.(*types.TypeParam); ok {
		return canonicalFromType(tp.Constraint(), forbiddenIfaces)
	}
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		var canon string
		if obj.Pkg() == nil {
			canon = obj.Name()
		} else {
			canon = obj.Pkg().Path() + "." + obj.Name()
		}
		if rawPublicOptionForbidden[canon] {
			return canon
		}
		// Named, but not in the forbidden set — it might be a local
		// interface that embeds a forbidden one (`type LocalRawPub
		// interface { outbox.Publisher }`). Recurse into the underlying
		// to detect that wrapping; for non-interface named types the
		// recursion returns "" via the no-match tail and we fall back to
		// the local canonical name (which is harmless — the caller only
		// triggers a violation when the canonical hits the forbidden
		// set, so a non-forbidden local name stays non-forbidden).
		//
		// Skip the recursion for sealed-marker patterns: a named
		// interface that declares its own unexported explicit method
		// (e.g. `sealedCellTxManager()`) is the wrapper itself, not a
		// forbidden bypass. CellTxManager / CellPublisher / CellWriter
		// embed the raw forbidden type but are the legitimate transport
		// across the cell boundary — recursing would falsely flag every
		// well-behaved sealed-marker With* signature.
		if iface, ok := named.Underlying().(*types.Interface); ok && !hasUnexportedExplicitMethod(iface) {
			if inner := canonicalFromType(iface, forbiddenIfaces); inner != "" {
				return inner
			}
		}
		return canon
	}
	if iface, ok := t.(*types.Interface); ok {
		// 1. Embed walk — prefer a forbidden embed when present so
		// `interface{ outbox.Publisher; otherIface }` is caught regardless of
		// declaration order.
		var firstNonForbidden string
		for i := 0; i < iface.NumEmbeddeds(); i++ {
			canon := canonicalFromType(iface.EmbeddedType(i), forbiddenIfaces)
			if canon == "" {
				continue
			}
			if rawPublicOptionForbidden[canon] {
				return canon
			}
			if firstNonForbidden == "" {
				firstNonForbidden = canon
			}
		}
		if firstNonForbidden != "" {
			return firstNonForbidden
		}
		// 2. Method-set fall-through — for anonymous interfaces with no
		// embedded named types (NumEmbeddeds()==0), check whether the
		// parameter type implements any forbidden interface. Catches the
		// pure-method bypass `interface{ RunInTx(...) error }` whose method
		// set matches persistence.TxRunner exactly.
		for canon, fIface := range forbiddenIfaces {
			if fIface == nil || fIface == iface {
				continue
			}
			// ImplementsInterfaceExact (value-only, no pointer fallback):
			// t here is often itself *types.Interface (anonymous-interface
			// param); a synthetic pointer-to-interface check is meaningless.
			if typesutil.ImplementsInterfaceExact(t, fIface) {
				return canon
			}
		}
	}
	return ""
}

// hasUnexportedExplicitMethod reports whether the interface declares at
// least one unexported method on itself (not via embedding). This is the
// sealed-marker discriminator: `interface { Publisher; sealedCellPublisher() }`
// has an unexported explicit method and is the legitimate wrapper; a
// plain `interface { Publisher }` does not and is a bypass attempt.
func hasUnexportedExplicitMethod(iface *types.Interface) bool {
	for i := 0; i < iface.NumExplicitMethods(); i++ {
		if !iface.ExplicitMethod(i).Exported() {
			return true
		}
	}
	return false
}

// loadForbiddenIfacesFromPkg resolves rawPublicOptionForbidden canonicals
// into *types.Interface values by walking the import graph reachable from
// pkg. Uses pkg.Imports() transitively to visit all dependency packages.
// Performed once per Pass; missing entries are silently skipped — the
// embed-walk path still covers them by canonical name.
func loadForbiddenIfacesFromPkg(pkg *types.Package) map[string]*types.Interface {
	out := make(map[string]*types.Interface, len(rawPublicOptionForbidden))
	wantByPath := make(map[string][]string, len(rawPublicOptionForbidden))
	for canonical := range rawPublicOptionForbidden {
		dot := strings.LastIndex(canonical, ".")
		if dot < 0 {
			continue
		}
		wantByPath[canonical[:dot]] = append(wantByPath[canonical[:dot]], canonical)
	}

	// Walk transitively reachable imports via BFS.
	visited := make(map[string]bool)
	queue := []*types.Package{pkg}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil || visited[cur.Path()] {
			continue
		}
		visited[cur.Path()] = true

		canonicals, ok := wantByPath[cur.Path()]
		if ok {
			for _, canonical := range canonicals {
				if _, already := out[canonical]; already {
					continue
				}
				dot := strings.LastIndex(canonical, ".")
				typeName := canonical[dot+1:]
				obj := cur.Scope().Lookup(typeName)
				if obj == nil {
					continue
				}
				if iface, ok := obj.Type().Underlying().(*types.Interface); ok {
					out[canonical] = iface
				}
			}
		}

		for _, imp := range cur.Imports() {
			if !visited[imp.Path()] {
				queue = append(queue, imp)
			}
		}
	}
	return out
}

// scanPassForRawPublicOption is the inner walker for a single Pass. When
// restrictToCellRoots is true, only files matching isCellSubtreeFile are
// scanned (the real-repo invariant); when false, all files in the Pass are
// scanned (the fixture detection test).
func scanPassForRawPublicOption(p *Pass, restrictToCellRoots bool) []rawPublicOptionViolation {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}
	// Lazy-load the forbidden interface types once per Pass so the
	// method-set fall-through (types.Implements) and named-underlying
	// recursion can detect anonymous and named local-interface bypasses.
	forbiddenIfaces := loadForbiddenIfacesFromPkg(p.Pkg)

	var out []rawPublicOptionViolation
	for _, file := range p.Files {
		relSlash := p.Rel(file)
		if restrictToCellRoots && !isCellSubtreeFile(relSlash) {
			continue
		}
		EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Recv != nil || !fn.Name.IsExported() ||
				!strings.HasPrefix(fn.Name.Name, "With") {
				return
			}
			if fn.Type.Params == nil {
				return
			}
			idx := 0
			for _, field := range fn.Type.Params.List {
				canonical := publicOptionParamCanonical(p.TypesInfo, field.Type, forbiddenIfaces)
				count := len(field.Names)
				if count == 0 {
					count = 1
				}
				for k := 0; k < count; k++ {
					if rawPublicOptionForbidden[canonical] {
						out = append(out, rawPublicOptionViolation{
							File:       relSlash,
							Line:       p.Fset.Position(field.Pos()).Line,
							FuncName:   fn.Name.Name,
							ParamType:  canonical,
							ParamIndex: idx,
						})
					}
					idx++
				}
			}
		})
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

	var violations []rawPublicOptionViolation
	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		violations = append(violations, scanPassForRawPublicOption(p, true)...)
		return nil
	})

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

	var violations []rawPublicOptionViolation
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: []string{"archtest_fixture"}},
		[]string{"./tools/archtest/internal/rawparamfixture"},
		func(p *Pass) []Diagnostic {
			violations = append(violations, scanPassForRawPublicOption(p, false)...)
			return nil
		})

	require.Len(t, violations, expectedRawParamFixtureViolations,
		"fixture must yield 10 violations: WithBadTxRunner / WithBadPublisher / "+
			"WithBadWriter / WithAliasedBadTxRunner (4 baseline) + "+
			"WithBadEmbedPublisher / WithBadEmbedWriter / WithBadEmbedTxRunner "+
			"(3 inline-interface-embed forms) + WithBadPureMethodIfaceTxRunner "+
			"(1 pure-method anonymous interface) + "+
			"WithBadNamedLocalEmbedPublisher (1 named local interface that embeds "+
			"outbox.Publisher — recursive underlying inspection) + "+
			"WithGenericTx (1 generic type param constrained to persistence.TxRunner)")

	got := map[string]string{}
	for _, v := range violations {
		got[v.FuncName] = v.ParamType
	}
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", got["WithBadTxRunner"])
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/outbox.Publisher", got["WithBadPublisher"])
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/outbox.Writer", got["WithBadWriter"])
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", got["WithAliasedBadTxRunner"],
		"types.Unalias must resolve type alias to canonical raw type")
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/outbox.Publisher", got["WithBadEmbedPublisher"],
		"inline interface embed must resolve to embedded raw type")
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/outbox.Writer", got["WithBadEmbedWriter"],
		"inline interface embed must resolve to embedded raw type")
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", got["WithBadEmbedTxRunner"],
		"inline interface embed must resolve to embedded raw type")
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", got["WithBadPureMethodIfaceTxRunner"],
		"pure-method anonymous interface must resolve via types.Implements fall-through")
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/outbox.Publisher", got["WithBadNamedLocalEmbedPublisher"],
		"named local interface that embeds outbox.Publisher must resolve via underlying recursion")
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", got["WithGenericTx"],
		"generic type param constrained to TxRunner must resolve via TypeParam.Constraint()")
}
