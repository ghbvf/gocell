package governance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// FMT-18 SPEC-CONTRACT-SYNC — cross-check wrapper.ContractSpec literals and
// wrapper.EventSpec(...) calls in cells/** against contracts/**/contract.yaml.
// Ensures ID, Kind, Method, Path (for http kind), Topic (for event kind)
// match the authoritative YAML. examples/** is exempted because its demo
// routes often do not have backing YAML by design (PR-A11 round-4 ADR §2).
//
// Implementation is go/ast based so field values provided via package-level
// string constants (e.g. `Path: pathUserByID` where pathUserByID is a
// `const = "..."`) are resolved at scan time, matching how reviewers actually
// read the code. A regex-only scanner would silently skip any non-literal
// field value — the gap that motivated re-implementing the rule on AST.
//
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
	codeFMT18 = "FMT-18"
	codeFMT19 = "FMT-19"
)

// contractSpecLiteral captures a parsed wrapper.ContractSpec{...} literal
// or wrapper.EventSpec(...) call from a source file scan.
//
// When the scanner finds an invocation whose key fields cannot be resolved
// to string literals (e.g. `wrapper.EventSpec(computeID(), ...)`), it
// records a literal with unresolved=true. The validation path surfaces
// such entries as visible warnings so the coverage gap is never silent.
type contractSpecLiteral struct {
	file       string
	line       int
	id         string
	kind       string
	method     string
	path       string
	topic      string
	unresolved bool
}

// validateFMT18 scans `cells/**/*.go` for wrapper.ContractSpec{} literals
// AND wrapper.EventSpec(...) calls, then cross-checks each against
// `contracts/**/contract.yaml`. Both struct fields and call arguments are
// resolved through package-level constants so `Path: pathUserByID` is
// equivalent to `Path: "/api/v1/access/users/{id}"`. Strict-only.
// examples/** is not scanned — demo surface contracts intentionally lack
// YAML backing.
func (v *Validator) validateFMT18(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	cellsDir := filepath.Join(v.root, "cells")
	literals, err := scanContractSpecLiterals(cellsDir)
	if err != nil {
		return []ValidationResult{
			v.newResult(codeFMT18, SeverityError, IssueInvalid,
				"cells/", "",
				fmt.Sprintf("FMT-18: failed to scan cells/ for wrapper.ContractSpec literals: %v", err)),
		}
	}

	var out []ValidationResult
	for _, lit := range literals {
		out = append(out, v.validateContractSpecLiteral(lit)...)
	}
	return out
}

func (v *Validator) validateContractSpecLiteral(lit contractSpecLiteral) []ValidationResult {
	if lit.unresolved {
		return []ValidationResult{v.newResult(codeFMT18, SeverityError, IssueInvalid,
			lit.file, "",
			fmt.Sprintf("FMT-18: %s:%d wrapper.EventSpec/ContractSpec argument could not be "+
				"resolved to a string literal — FMT-18 cannot cross-check against "+
				"contracts/**/contract.yaml. Use a string literal or a package-level "+
				"const in the same file.",
				lit.file, lit.line))}
	}
	if lit.id == "" {
		return nil
	}
	contract, ok := v.project.Contracts[lit.id]
	if !ok {
		return []ValidationResult{v.newResult(codeFMT18, SeverityError, IssueInvalid,
			lit.file, "",
			fmt.Sprintf("FMT-18: %s:%d references ContractSpec ID %q with no matching contracts/**/contract.yaml entry",
				lit.file, lit.line, lit.id))}
	}

	var out []ValidationResult
	if lit.kind != "" && lit.kind != contract.Kind {
		out = append(out, v.newResult(codeFMT18, SeverityError, IssueInvalid,
			lit.file, "",
			fmt.Sprintf("FMT-18: %s:%d ContractSpec Kind=%q disagrees with YAML Kind=%q for %q",
				lit.file, lit.line, lit.kind, contract.Kind, lit.id)))
	}
	out = append(out, v.validateHTTPContractSpecLiteral(lit, contract)...)
	return out
}

func (v *Validator) validateHTTPContractSpecLiteral(
	lit contractSpecLiteral,
	contract *metadata.ContractMeta,
) []ValidationResult {
	if contract.Kind != "http" || contract.Endpoints.HTTP == nil {
		return nil
	}
	h := contract.Endpoints.HTTP
	var out []ValidationResult
	if lit.method != "" && lit.method != h.Method {
		out = append(out, v.newResult(codeFMT18, SeverityError, IssueInvalid,
			lit.file, "",
			fmt.Sprintf("FMT-18: %s:%d ContractSpec Method=%q disagrees with YAML %q for %q",
				lit.file, lit.line, lit.method, h.Method, lit.id)))
	}
	if lit.path != "" && lit.path != h.Path {
		out = append(out, v.newResult(codeFMT18, SeverityError, IssueInvalid,
			lit.file, "",
			fmt.Sprintf("FMT-18: %s:%d ContractSpec Path=%q disagrees with YAML %q for %q",
				lit.file, lit.line, lit.path, h.Path, lit.id)))
	}
	return out
}

// scanContractSpecLiterals walks dir recursively and parses each non-test .go
// file via go/parser. Within each file, wrapper.ContractSpec composite
// literals and wrapper.EventSpec call expressions are extracted; field
// values referencing package-level string constants are resolved so the
// extracted contractSpecLiteral carries the effective string value.
func scanContractSpecLiterals(dir string) ([]contractSpecLiteral, error) {
	var out []contractSpecLiteral
	fset := token.NewFileSet()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !shouldScanContractSpecFile(path, info) {
			return nil
		}
		lits, err := scanContractSpecFile(fset, path)
		if err != nil {
			return fmt.Errorf("scan %s: %w", path, err)
		}
		out = append(out, lits...)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

func shouldScanContractSpecFile(path string, info os.FileInfo) bool {
	return !info.IsDir() &&
		strings.HasSuffix(path, ".go") &&
		!strings.HasSuffix(path, "_test.go")
}

// scanContractSpecFile parses a single file and returns every
// wrapper.ContractSpec literal and wrapper.EventSpec call it finds, with
// string constants resolved.
func scanContractSpecFile(fset *token.FileSet, path string) ([]contractSpecLiteral, error) {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	wrapperAlias, ok := wrapperImportAlias(file)
	if !ok {
		// File does not import kernel/wrapper, so it cannot contain any
		// ContractSpec literal or EventSpec call worth scanning.
		return nil, nil
	}
	consts := collectPackageStringConsts(file)

	var out []contractSpecLiteral
	ast.Inspect(file, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.CompositeLit:
			if lit, ok := parseContractSpecCompositeLit(fset, path, expr, consts, wrapperAlias); ok {
				out = append(out, lit)
			}
		case *ast.CallExpr:
			if lit, ok := parseEventSpecCallExpr(fset, path, expr, consts, wrapperAlias); ok {
				out = append(out, lit)
			}
		}
		return true
	})
	return out, nil
}

// wrapperImportAlias resolves the local name a file uses to reference
// github.com/ghbvf/gocell/kernel/wrapper. Returns ok=false when the file
// does not import the package. Honours explicit aliases
// (`import kw "..../kernel/wrapper"` → "kw"), the default short name
// ("wrapper"), and dot-imports (".").
func wrapperImportAlias(file *ast.File) (string, bool) {
	const wrapperPkgPath = `"github.com/ghbvf/gocell/kernel/wrapper"`
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != wrapperPkgPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name, true
		}
		return "wrapper", true
	}
	return "", false
}

// collectPackageStringConsts returns package-level `const name = "..."`
// declarations whose rhs is a string literal. Both single-decl and grouped
// `const (...)` forms are collected.
func collectPackageStringConsts(file *ast.File) map[string]string {
	consts := make(map[string]string)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		collectConstSpecs(gen.Specs, consts)
	}
	return consts
}

// collectConstSpecs folds one const block's Specs into the consts map.
func collectConstSpecs(specs []ast.Spec, consts map[string]string) {
	for _, spec := range specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		collectValueSpec(vs, consts)
	}
}

// collectValueSpec extracts name=literal pairs from a single ValueSpec.
func collectValueSpec(vs *ast.ValueSpec, consts map[string]string) {
	for i, name := range vs.Names {
		if i >= len(vs.Values) {
			continue
		}
		if s, ok := stringLiteralValue(vs.Values[i]); ok {
			consts[name.Name] = s
		}
	}
}

// stringLiteralValue returns the unescaped string value if expr is a STRING
// BasicLit, otherwise ok=false. Delegates to strconv.Unquote so both
// double-quoted ("…") and backtick raw (`…`) forms with full Go escape
// semantics are handled consistently.
func stringLiteralValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// parseContractSpecCompositeLit extracts a contractSpecLiteral from a
// `wrapper.ContractSpec{...}` composite literal, returning ok=false for any
// other composite literal. Field values are resolved through the supplied
// consts map so `Path: pathUserByID` is equivalent to a string literal.
// wrapperAlias is the local name the file uses to reference kernel/wrapper
// (resolved via wrapperImportAlias).
func parseContractSpecCompositeLit(
	fset *token.FileSet,
	filePath string,
	expr *ast.CompositeLit,
	consts map[string]string,
	wrapperAlias string,
) (contractSpecLiteral, bool) {
	if !isWrapperSelector(expr.Type, "ContractSpec", wrapperAlias) {
		return contractSpecLiteral{}, false
	}
	lit := contractSpecLiteral{
		file: filePath,
		line: fset.Position(expr.Pos()).Line,
	}
	for _, elt := range expr.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		value, ok := resolveStringValue(kv.Value, consts)
		if !ok {
			continue
		}
		switch keyIdent.Name {
		case "ID":
			lit.id = value
		case "Kind":
			lit.kind = value
		case "Method":
			lit.method = value
		case "Path":
			lit.path = value
		case "Topic":
			lit.topic = value
		case "Transport":
			// Transport has no YAML cross-check yet — recorded for future use.
		}
	}
	return lit, true
}

// parseEventSpecCallExpr extracts a contractSpecLiteral from a
// `wrapper.EventSpec(id, transport)` call. Kind is synthesised as "event"
// and Topic == id by construction (the helper enforces id==topic).
//
// When the ID argument is not a string literal or resolvable local const
// (e.g. a cross-package const, a function call, or an unknown identifier),
// the scanner records a literal with id="" AND unresolved=true so the
// validation path can surface a visible warning instead of silently
// dropping the call from FMT-18's coverage.
func parseEventSpecCallExpr(
	fset *token.FileSet,
	filePath string,
	expr *ast.CallExpr,
	consts map[string]string,
	wrapperAlias string,
) (contractSpecLiteral, bool) {
	if !isWrapperSelector(expr.Fun, "EventSpec", wrapperAlias) {
		return contractSpecLiteral{}, false
	}
	if len(expr.Args) < 1 {
		return contractSpecLiteral{}, false
	}
	pos := fset.Position(expr.Pos()).Line
	id, resolved := resolveStringValue(expr.Args[0], consts)
	if !resolved {
		return contractSpecLiteral{
			file:       filePath,
			line:       pos,
			kind:       "event",
			unresolved: true,
		}, true
	}
	return contractSpecLiteral{
		file:  filePath,
		line:  pos,
		id:    id,
		kind:  "event",
		topic: id,
	}, true
}

// isWrapperSelector reports whether expr is a `<wrapperAlias>.<name>`
// selector expression. wrapperAlias is the local name the enclosing file
// binds to the kernel/wrapper import (resolved via wrapperImportAlias).
// Dot-imports bypass the selector form entirely; they are unsupported by
// this scanner and will produce neither false positives nor coverage.
func isWrapperSelector(expr ast.Expr, name, wrapperAlias string) bool {
	if wrapperAlias == "" || wrapperAlias == "." {
		return false
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == wrapperAlias && sel.Sel.Name == name
}

// resolveStringValue unwraps an expression to a string value. Supports:
//   - string literals (BasicLit STRING)
//   - package-level const identifiers declared in the same file
//   - parenthesised and simple binary concatenations of the above
//
// Returns ok=false for any form the scanner cannot prove is string-valued
// (function calls, cross-file constants, runtime-computed strings).
func resolveStringValue(expr ast.Expr, consts map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return stringLiteralValue(e)
	case *ast.Ident:
		s, ok := consts[e.Name]
		return s, ok
	case *ast.ParenExpr:
		return resolveStringValue(e.X, consts)
	case *ast.BinaryExpr:
		return resolveBinaryConcat(e, consts)
	}
	return "", false
}

// resolveBinaryConcat handles `a + b` expressions where both sides must
// resolve to strings. Non-ADD operators and any non-string operand cause
// ok=false.
func resolveBinaryConcat(e *ast.BinaryExpr, consts map[string]string) (string, bool) {
	if e.Op != token.ADD {
		return "", false
	}
	left, ok := resolveStringValue(e.X, consts)
	if !ok {
		return "", false
	}
	right, ok := resolveStringValue(e.Y, consts)
	if !ok {
		return "", false
	}
	return left + right, true
}

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
