package governance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// FMT-18 SPEC-CONTRACT-SYNC was removed in PR-V1-CODEGEN-FULL-MIGRATION (W4).
// After W3 completed the cell-by-cell migration, cells/** contains 0
// wrapper.ContractSpec literals — enforced statically by three archtest gates:
//   - CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01
//   - NO-MANUAL-CONTRACTSPEC-LITERAL-01
//   - EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
//
// The FMT-18 AST scanner's scan target (cells/**) is now always empty, making
// the rule a no-op. The archtest gates provide stronger, faster enforcement
// (import-graph level vs AST text scan). FMT-18 is deleted; the archtest gates
// are the authoritative guardians of the cells-no-manual-spec invariant.

// FMT-19 WRAPPER-NO-PACKAGE-STATE — enforces that kernel/wrapper/*.go
// contains no mutable package-level variables of interface or pointer
// type. Immutable zero-value sentinels (NoopTracer{}, noopSpan{}) and
// compile-time interface checks (var _ Tracer = NoopTracer{}) are
// allowed; any `var x Tracer` / `var mu sync.Mutex` is rejected. Guards
// the round-4 invariant that kernel/wrapper is a pure value+rules layer.
//
// Both rules are strict-only (surface only under ValidateStrict(true)) to
// avoid disrupting the base Validate() path for rapid iteration.

const (
	codeFMT19 = "FMT-19"
)

// FMT-19 AST rewrite (PR246-FU1 finding ③):
//
//   - Accept rule ①: `var _ Type = expr` (blank-identifier compile-time
//     interface/typecheck — all Names must be '_').
//   - Accept rule ②: `var name [Type] = CompositeLit{}` where the initializer
//     is a composite literal with zero Elts and a plain struct type (identifier
//     or selector expression). Slice/map/chan/pointer composite literals are
//     rejected even when empty — they are reference types.
//   - Reject everything else structurally (no hard-coded type whitelist).
//
// The pre-FU1 line-regex + fmt19KnownValueTypes whitelist missed grouped
// `var (...)` blocks, no-initializer vars, multi-name declarations, and
// mutable container types (map/chan/slice); the AST rewrite closes all
// five evasion classes by scanning the syntax tree directly.
func (v *Validator) validateFMT19(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	dir := filepath.Join(v.root, "kernel", "wrapper")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []ValidationResult{
			v.newResult(codeFMT19, SeverityError, IssueInvalid,
				"kernel/wrapper/", "",
				fmt.Sprintf("FMT-19: failed to read kernel/wrapper/: %v", err)),
		}
	}

	fset := token.NewFileSet()
	var out []ValidationResult
	for _, entry := range entries {
		if !shouldScanWrapperFile(entry) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		out = append(out, v.scanWrapperPackageStateFile(fset, path)...)
	}
	return out
}

func shouldScanWrapperFile(entry os.DirEntry) bool {
	name := entry.Name()
	return !entry.IsDir() &&
		strings.HasSuffix(name, ".go") &&
		!strings.HasSuffix(name, "_test.go")
}

func (v *Validator) scanWrapperPackageStateFile(fset *token.FileSet, path string) []ValidationResult {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return []ValidationResult{v.newResult(codeFMT19, SeverityError, IssueInvalid,
			path, "",
			fmt.Sprintf("FMT-19: failed to parse %s: %v", path, err))}
	}

	var out []ValidationResult
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if reason, forbidden := classifyWrapperVarSpec(vs); forbidden {
				nameList := formatVarSpecNames(vs)
				out = append(out, v.newResult(codeFMT19, SeverityError, IssueInvalid,
					path, "",
					fmt.Sprintf("FMT-19: %s:%d forbids package-level var %s — %s "+
						"(kernel/wrapper must stay stateless: round-4 constructor-injection invariant)",
						path, fset.Position(vs.Pos()).Line, nameList, reason)))
			}
		}
	}
	return out
}

// classifyWrapperVarSpec returns (violationReason, forbidden). Accept rules:
//
//	① all Names are blank — compile-time interface check, any RHS allowed.
//	② single Name + single Value that, after unwrapping any number of
//	   *ast.ParenExpr layers, is a composite literal with zero Elts on a
//	   plain struct type (identifier or selector expression).
//
// Everything else is forbidden — the kernel/wrapper package may only hold
// blank-ident interface checks and zero-value sentinels.
func classifyWrapperVarSpec(vs *ast.ValueSpec) (string, bool) {
	if allBlank(vs.Names) {
		return "", false
	}
	if len(vs.Names) > 1 {
		return "multi-name declaration forbidden (use separate var blocks or move to constants)", true
	}
	if len(vs.Values) == 0 {
		return "no initializer — implicit zero-value may be a mutable reference (map/chan/slice/interface)", true
	}
	cl, ok := unwrapCompositeLit(vs.Values[0])
	if !ok {
		return "initializer is not a composite literal — only zero-value `Type{}` sentinels allowed at package scope", true
	}
	if len(cl.Elts) > 0 {
		return "initializer is a non-empty composite literal — only zero-value (empty) sentinels allowed", true
	}
	// Reject slice/map/chan/pointer composite literals (still reference types even when empty).
	if !isPlainStructCompositeType(cl.Type) {
		return "initializer is a composite of a reference/container type — only plain struct zero-value sentinels allowed", true
	}
	return "", false
}

// unwrapCompositeLit strips any number of *ast.ParenExpr layers around expr
// and returns the inner *ast.CompositeLit if found. Returns (nil, false)
// for any other expression shape (idents, calls, unary expressions like
// `&T{}`, function literals).
func unwrapCompositeLit(expr ast.Expr) (*ast.CompositeLit, bool) {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	cl, ok := expr.(*ast.CompositeLit)
	return cl, ok
}

func allBlank(names []*ast.Ident) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if n.Name != "_" {
			return false
		}
	}
	return true
}

// isPlainStructCompositeType reports whether expr is an ast type that, when
// used as a CompositeLit's Type, names a plain struct (ident or pkg.ident)
// rather than a reference/container type (map, slice, chan, pointer, array).
//
// At top-level VAR specs, CompositeLit.Type is always set by the parser:
// both `var x T = T{}` and `var x = T{}` record the type on the
// CompositeLit. The nil case only arises for nested implicit composite
// literals (e.g. inner `{}` in `[]T{{}, {}}`); those have non-empty Elts
// in the outer literal and never reach this helper. nil is therefore
// rejected defensively rather than accepted — fewer paths, no implicit
// trust in the upstream zero-Elts check.
func isPlainStructCompositeType(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	switch expr.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return true
	default:
		return false
	}
}

func formatVarSpecNames(vs *ast.ValueSpec) string {
	if len(vs.Names) == 0 {
		return "<anon>"
	}
	names := make([]string, 0, len(vs.Names))
	for _, n := range vs.Names {
		names = append(names, n.Name)
	}
	return strings.Join(names, ", ")
}
