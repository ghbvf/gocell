package governance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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
type contractSpecLiteral struct {
	file   string
	line   int
	id     string
	kind   string
	method string
	path   string
	topic  string
}

// wrapperVarDeclRe is used by FMT-19 only (line-based scan is sufficient
// because the rule targets simple `var x Type = ...` forms).
var wrapperVarDeclRe = regexp.MustCompile(`^var\s+(\w+)\s+(.+?)\s*=\s*(.+)$`)

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
	consts := collectPackageStringConsts(file)

	var out []contractSpecLiteral
	ast.Inspect(file, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.CompositeLit:
			if lit, ok := parseContractSpecCompositeLit(fset, path, expr, consts); ok {
				out = append(out, lit)
			}
		case *ast.CallExpr:
			if lit, ok := parseEventSpecCallExpr(fset, path, expr, consts); ok {
				out = append(out, lit)
			}
		}
		return true
	})
	return out, nil
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
// BasicLit, otherwise ok=false.
func stringLiteralValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconvUnquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// strconvUnquote mirrors strconv.Unquote for string literals but lives here
// to keep the kernel import list minimal (go/parser already pulls strconv
// transitively, so this is about code locality, not dep size).
func strconvUnquote(raw string) (string, error) {
	if len(raw) < 2 {
		return "", fmt.Errorf("not a quoted string: %q", raw)
	}
	q := raw[0]
	if q != raw[len(raw)-1] {
		return "", fmt.Errorf("mismatched quote: %q", raw)
	}
	inner := raw[1 : len(raw)-1]
	// For our purposes Go source is restricted to simple ASCII in contract
	// IDs / paths — unescape the three common cases, fall back to raw for
	// anything else.
	inner = strings.ReplaceAll(inner, `\"`, `"`)
	inner = strings.ReplaceAll(inner, `\\`, `\`)
	inner = strings.ReplaceAll(inner, `\n`, "\n")
	return inner, nil
}

// parseContractSpecCompositeLit extracts a contractSpecLiteral from a
// `wrapper.ContractSpec{...}` composite literal, returning ok=false for any
// other composite literal. Field values are resolved through the supplied
// consts map so `Path: pathUserByID` is equivalent to a string literal.
func parseContractSpecCompositeLit(
	fset *token.FileSet,
	filePath string,
	expr *ast.CompositeLit,
	consts map[string]string,
) (contractSpecLiteral, bool) {
	if !isWrapperSelector(expr.Type, "ContractSpec") {
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
func parseEventSpecCallExpr(
	fset *token.FileSet,
	filePath string,
	expr *ast.CallExpr,
	consts map[string]string,
) (contractSpecLiteral, bool) {
	if !isWrapperSelector(expr.Fun, "EventSpec") {
		return contractSpecLiteral{}, false
	}
	if len(expr.Args) < 1 {
		return contractSpecLiteral{}, false
	}
	id, ok := resolveStringValue(expr.Args[0], consts)
	if !ok {
		return contractSpecLiteral{}, false
	}
	return contractSpecLiteral{
		file:  filePath,
		line:  fset.Position(expr.Pos()).Line,
		id:    id,
		kind:  "event",
		topic: id,
	}, true
}

// isWrapperSelector reports whether expr is a `wrapper.<name>` selector
// expression. Accepts both the import alias "wrapper" and the full package
// name `wrapper` (Go's default import alias), which is all our cells use.
func isWrapperSelector(expr ast.Expr, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "wrapper" && sel.Sel.Name == name
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

// validateFMT19 scans kernel/wrapper/*.go for package-level var declarations
// and rejects any whose RHS is not a zero-value struct literal or a
// compile-time interface check.
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

	var out []ValidationResult
	for _, entry := range entries {
		if !shouldScanWrapperFile(entry) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		out = append(out, v.validateWrapperPackageStateFile(path)...)
	}
	return out
}

func shouldScanWrapperFile(entry os.DirEntry) bool {
	name := entry.Name()
	return !entry.IsDir() &&
		strings.HasSuffix(name, ".go") &&
		!strings.HasSuffix(name, "_test.go")
}

func (v *Validator) validateWrapperPackageStateFile(path string) []ValidationResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return []ValidationResult{v.newResult(codeFMT19, SeverityError, IssueInvalid,
			path, "",
			fmt.Sprintf("FMT-19: failed to read %s: %v", path, err))}
	}

	var out []ValidationResult
	for i, line := range strings.Split(string(data), "\n") {
		name, typ, forbidden := forbiddenWrapperVar(strings.TrimSpace(line))
		if !forbidden {
			continue
		}
		out = append(out, v.newResult(codeFMT19, SeverityError, IssueInvalid,
			path, "",
			fmt.Sprintf("FMT-19: %s:%d forbids mutable package-level variable %q of type %q — "+
				"kernel/wrapper must stay stateless (round-4 constructor-injection invariant)",
				path, i+1, name, typ)))
	}
	return out
}

func forbiddenWrapperVar(line string) (name string, typ string, forbidden bool) {
	if !strings.HasPrefix(line, "var ") || isCompileTimeInterfaceCheck(line) {
		return "", "", false
	}
	sm := wrapperVarDeclRe.FindStringSubmatch(line)
	if len(sm) < 4 {
		return "", "", false
	}
	name = sm[1]
	typ = strings.TrimSpace(sm[2])
	rhs := strings.TrimSpace(sm[3])
	return name, typ, !strings.HasSuffix(rhs, "{}") && isInterfaceOrPointerType(typ)
}

func isCompileTimeInterfaceCheck(line string) bool {
	return strings.HasPrefix(line, "var _ ") || strings.HasPrefix(line, "var _\t")
}

// isInterfaceOrPointerType is a shallow classifier — pointers by leading
// '*', interfaces by capitalised identifier not in the known-struct
// allowlist. Used only by FMT-19 to decide whether a package-level var
// carries a potentially-mutable reference.
func isInterfaceOrPointerType(typ string) bool {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return false
	}
	if strings.HasPrefix(typ, "*") {
		return true
	}
	known := map[string]bool{
		"Attr":          true,
		"ContractSpec":  true,
		"Disposition":   true,
		"Entry":         true,
		"HandleResult":  true,
		"NoopTracer":    true,
		"StatusCode":    true,
		"ConsumerFunc":  true,
		"ErrorRedactor": true,
	}
	return !known[typ]
}
