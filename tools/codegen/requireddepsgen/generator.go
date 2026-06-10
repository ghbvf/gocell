// Package requireddepsgen generates validateRequired() methods for Service
// structs by reading gocell:"required" struct field tags.
//
// Usage:
//
//	out, err := requireddepsgen.Generate("/path/to/slice")
//	// out contains the formatted Go source for service_required_gen.go
//
// # Primary Tag
//
// Mark a Service struct field as required:
//
//	type Service struct {
//	    repo domain.Repository `gocell:"required"`
//	}
//
// The generator emits a validateRequired() method that checks each tagged
// field and returns an errcode.Error on the first nil dependency found.
//
// # Sub-Tags (all optional)
//
// Sub-tags customize the generated error for a specific field.
// They have no effect on fields without gocell:"required".
//
//	gocellKind   errcode.Kind constant name (default: KindInternal)
//	              e.g. gocellKind:"KindInvalid"
//
//	gocellCode   errcode error code constant (default: ErrCellInvalidConfig)
//	              e.g. gocellCode:"ErrValidationFailed"
//
//	gocellErr    error message string literal
//	              (default: "{pkg}.NewService: {field} required")
//	              e.g. gocellErr:"slicepkg: TxRunner required; use WithTxManager"
//
// Typical case (default KindInternal/ErrCellInvalidConfig with custom message):
//
//	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"mypkg: TxRunner required; use WithTxManager"`
//
// Overriding all three sub-tags (rare — different HTTP status intentional):
//
//	codec *query.CursorCodec `gocell:"required" gocellCode:"ErrCellMissingCodec" gocellErr:"mypkg: cursor codec required"`
package requireddepsgen

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Sentinel errors returned by Generate. Declared via errcode.New (not
// errors.New) so exported package-scope sentinels participate in the project
// error taxonomy — EXPORTED-ERROR-NEW-01 bans exported errors.New sentinels
// module-wide, tools/ included. They keep stable pointer identity (single
// package-var construction), so errors.Is against them is unaffected.
var (
	// ErrNoServiceStruct is returned when service.go contains no
	// "type Service struct" declaration.
	ErrNoServiceStruct = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"requireddepsgen: no Service struct in service.go")

	// ErrUnknownTagValue is returned when a struct field has a gocell tag
	// whose value is not "" or "required", or when gocellKind/gocellCode tags
	// contain values that do not match the errcode identifier whitelist.
	ErrUnknownTagValue = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"requireddepsgen: unknown gocell tag value (allowed: \"\", \"required\")")

	// ErrBadTagSyntax is returned when a struct field carries a malformed
	// struct tag (one reflect.StructTag.Lookup would silently drop). Surfacing
	// it fail-closed prevents a typo'd tag from silently skipping a required
	// dependency guard.
	ErrBadTagSyntax = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"requireddepsgen: malformed struct tag")
)

// errcodeKindRE and errcodeCodeRE match valid errcode identifier overrides for
// the gocellKind and gocellCode tags respectively. Values must be qualified
// identifiers of the form errcode.Kind<Name> (kind) or errcode.Err<Name>
// (code) to prevent code injection via struct tags AND to prevent a kind being
// supplied where a code is expected (or vice versa). Any other value — including
// a well-formed errcode identifier of the wrong family — is rejected with
// ErrUnknownTagValue.
var (
	errcodeKindRE = regexp.MustCompile(`^errcode\.Kind[A-Z][A-Za-z0-9]*$`)
	errcodeCodeRE = regexp.MustCompile(`^errcode\.Err[A-Z][A-Za-z0-9]*$`)
)

const (
	modulePrefix     = "github.com/ghbvf/gocell"
	errcodeImport    = modulePrefix + "/pkg/errcode"
	validationImport = modulePrefix + "/pkg/validation"

	defaultKind = "errcode.KindInternal"
	defaultCode = "errcode.ErrCellInvalidConfig"
)

// fieldGuard holds the parsed information for a single required field.
type fieldGuard struct {
	name      string
	isPointer bool
	kindExpr  string
	codeExpr  string
	errMsg    string
}

// Opts controls optional generator behavior.
//
// BuildTag, when non-empty, emits a `//go:build <BuildTag>` header at the top
// of the generated file. Used for archtest fixtures that must carry
// //go:build archtest_fixture. Production calls leave it empty.
type Opts struct {
	BuildTag string
}

// Generate reads slicePath/service.go, scans the Service struct for
// gocell:"required" field tags, and returns the formatted Go source for
// service_required_gen.go.
//
// Returns byte-identical output for identical inputs (no map iteration order
// dependency). Field guards appear in struct declaration order.
func Generate(slicePath string) ([]byte, error) {
	return GenerateWithOpts(slicePath, Opts{})
}

// GenerateWithOpts is the option-aware variant of Generate. See Opts.
func GenerateWithOpts(slicePath string, opts Opts) ([]byte, error) {
	serviceFile := filepath.Join(slicePath, "service.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, serviceFile, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("requireddepsgen: parse %s: %w", serviceFile, err)
	}

	pkgName, guards, err := extractServiceGuards(file)
	if err != nil {
		return nil, err
	}

	return renderOutput(pkgName, guards, opts)
}

// GenerateAll walks modRoot for all cells/*/slices/*/service.go files and
// returns a map from slice directory path to generated file contents.
//
// Slices whose service.go does not declare a "Service" struct are silently
// skipped (ErrNoServiceStruct). All other errors are fatal.
func GenerateAll(modRoot string) (map[string][]byte, error) {
	slicePaths, err := FindSlicePaths(modRoot)
	if err != nil {
		return nil, fmt.Errorf("requireddepsgen: walk %s: %w", modRoot, err)
	}

	result := make(map[string][]byte, len(slicePaths))
	for _, sp := range slicePaths {
		out, err := Generate(sp)
		if errors.Is(err, ErrNoServiceStruct) {
			continue // service.go exists but has no Service struct — skip
		}
		if err != nil {
			return nil, fmt.Errorf("requireddepsgen: generate for %s: %w", sp, err)
		}
		result[sp] = out
	}
	return result, nil
}

// extractServiceGuards parses the AST file, locates the Service struct, and
// returns the package name and the list of field guards in declaration order.
func extractServiceGuards(file *ast.File) (string, []fieldGuard, error) {
	pkgName := file.Name.Name
	structType, found := findServiceStruct(file)
	if !found {
		return "", nil, ErrNoServiceStruct
	}

	guards, err := parseStructFields(pkgName, structType)
	if err != nil {
		return "", nil, err
	}
	return pkgName, guards, nil
}

// findServiceStruct locates "type Service struct" in the file.
func findServiceStruct(file *ast.File) (*ast.StructType, bool) {
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Service" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if ok {
				return st, true
			}
		}
	}
	return nil, false
}

// parseStructFields iterates the struct fields and collects required-dep guards.
func parseStructFields(pkgName string, st *ast.StructType) ([]fieldGuard, error) {
	var guards []fieldGuard
	for _, field := range st.Fields.List {
		if field.Tag == nil {
			continue
		}

		rawTag := strings.Trim(field.Tag.Value, "`")
		// reflect.StructTag.Get silently treats a malformed tag as absent,
		// which would drop a required-dep guard without a trace. Fail closed:
		// any malformed tag on a Service field is a hard error.
		if !TagSyntaxValid(rawTag) {
			return nil, fmt.Errorf("%w: %q", ErrBadTagSyntax, rawTag)
		}
		tag := reflect.StructTag(rawTag)

		gocellVal := tag.Get("gocell")
		switch gocellVal {
		case "":
			continue
		case "required":
			// proceed
		default:
			return nil, fmt.Errorf("%w: got %q", ErrUnknownTagValue, gocellVal)
		}

		// For multi-name fields (rare, but handle gracefully), generate one
		// guard per name.
		for _, ident := range field.Names {
			g, err := buildGuard(pkgName, ident.Name, field.Type, tag)
			if err != nil {
				return nil, errors.Join(ErrUnknownTagValue, err)
			}
			guards = append(guards, g)
		}
	}
	return guards, nil
}

// TagSyntaxValid reports whether raw (the unquoted struct-tag body, e.g.
// `json:"x" gocell:"required"`) is a well-formed sequence of conventional
// key:"value" pairs. It mirrors the scan loop of reflect.StructTag.Lookup but,
// unlike Lookup — which silently stops at the first malformed pair and reports
// the key as absent — returns false on any malformation so callers can fail
// closed. Exported so the REQUIRED-DEP-NIL-GUARD-01 A4 archtest shares this
// single malformed-tag definition rather than re-deriving it.
// ref: Go src reflect/type.go StructTag.Lookup.
func TagSyntaxValid(raw string) bool {
	tag := raw
	for tag != "" {
		rest, ok := consumeOneTagPair(tag)
		if !ok {
			return false
		}
		tag = rest
	}
	return true
}

// consumeOneTagPair skips leading spaces and consumes one conventional
// key:"value" pair from tag, returning the remainder. ok=false on any
// malformation. A remainder of only spaces returns ("", true) so the
// TagSyntaxValid loop terminates as valid.
func consumeOneTagPair(tag string) (rest string, ok bool) {
	for len(tag) > 0 && tag[0] == ' ' {
		tag = tag[1:]
	}
	if tag == "" {
		return "", true
	}
	// Scan the key up to the colon. A space, quote or control char before ':',
	// an empty key, or a colon not followed by '"' is malformed.
	i := 0
	for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
		i++
	}
	if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
		return "", false
	}
	return consumeQuotedValue(tag[i+1:])
}

// consumeQuotedValue consumes a leading Go-quoted string from tag (which begins
// at the opening '"') and returns the remainder. ok=false when the quote is
// unterminated or the quoted form does not unquote.
func consumeQuotedValue(tag string) (rest string, ok bool) {
	i := 1
	for i < len(tag) && tag[i] != '"' {
		if tag[i] == '\\' {
			i++
		}
		i++
	}
	if i >= len(tag) {
		return "", false
	}
	if _, err := strconv.Unquote(tag[:i+1]); err != nil {
		return "", false
	}
	return tag[i+1:], true
}

// buildGuard constructs the fieldGuard for a single named field.
// Returns an error when gocellKind or gocellCode values do not match the
// errcode identifier whitelist (errcode.Kind* or errcode.Err*), preventing
// code injection via maliciously crafted struct tags.
func buildGuard(pkgName, fieldName string, fieldType ast.Expr, tag reflect.StructTag) (fieldGuard, error) {
	isPtr := isPointerType(fieldType)

	kind, err := resolveTagOrDefault(tag.Get("gocellKind"), defaultKind, "errcode.", errcodeKindRE)
	if err != nil {
		return fieldGuard{}, fmt.Errorf("field %s gocellKind: %w", fieldName, err)
	}
	code, err := resolveTagOrDefault(tag.Get("gocellCode"), defaultCode, "errcode.", errcodeCodeRE)
	if err != nil {
		return fieldGuard{}, fmt.Errorf("field %s gocellCode: %w", fieldName, err)
	}
	msg := tag.Get("gocellErr")
	if msg == "" {
		msg = pkgName + ".NewService: " + fieldName + " required"
	}

	return fieldGuard{
		name:      fieldName,
		isPointer: isPtr,
		kindExpr:  kind,
		codeExpr:  code,
		errMsg:    msg,
	}, nil
}

// isPointerType reports whether the AST type expression is a pointer (*T).
// SelectorExpr (pkg.Type) and Ident are treated as interface types.
func isPointerType(expr ast.Expr) bool {
	_, ok := expr.(*ast.StarExpr)
	return ok
}

// resolveTagOrDefault qualifies a bare identifier with prefix if not already
// qualified, falling back to def when val is empty. Returns ErrUnknownTagValue
// when the resulting qualified identifier does not match re — the family-specific
// whitelist (errcodeKindRE for gocellKind, errcodeCodeRE for gocellCode). This
// rejects both code injection via maliciously crafted tags AND a kind supplied
// where a code is expected (or vice versa).
func resolveTagOrDefault(val, def, prefix string, re *regexp.Regexp) (string, error) {
	if val == "" {
		return def, nil
	}
	qualified := val
	if !strings.HasPrefix(val, prefix) {
		qualified = prefix + val
	}
	if !re.MatchString(qualified) {
		return "", fmt.Errorf("%w: %q is not a valid errcode identifier for this tag (must match %s)",
			ErrUnknownTagValue, qualified, re.String())
	}
	return qualified, nil
}

// renderOutput builds the Go source for the generated file.
func renderOutput(pkgName string, guards []fieldGuard, opts Opts) ([]byte, error) {
	needsErrcode := len(guards) > 0
	needsValidation := false
	for _, g := range guards {
		if !g.isPointer {
			needsValidation = true
			break
		}
	}

	var buf bytes.Buffer
	if opts.BuildTag != "" {
		buf.WriteString("//go:build ")
		buf.WriteString(opts.BuildTag)
		buf.WriteString("\n\n")
	}
	buf.WriteString("// Code generated by gocell generate required-deps. DO NOT EDIT.\n")
	buf.WriteString("\n")
	buf.WriteString("package ")
	buf.WriteString(pkgName)
	buf.WriteString("\n")

	if needsErrcode || needsValidation {
		buf.WriteString("\nimport (\n")
		if needsErrcode {
			buf.WriteString("\t\"" + errcodeImport + "\"\n")
		}
		if needsValidation {
			buf.WriteString("\t\"" + validationImport + "\"\n")
		}
		buf.WriteString(")\n")
	}

	buf.WriteString(`
// validateRequired verifies every required dependency on the Service struct
// is present and non-typed-nil. Generated from gocell:"required" struct field
// tags; do not edit by hand.
func (s *Service) validateRequired() error {
`)

	for _, g := range guards {
		writeGuard(&buf, g)
	}

	buf.WriteString("\treturn nil\n}\n")

	return format.Source(buf.Bytes())
}

// writeGuard writes a single nil-check guard to buf.
// strconv.Quote is used for the error message string to correctly handle any
// special characters (e.g. backslashes, embedded quotes) without producing
// malformed Go source.
func writeGuard(buf *bytes.Buffer, g fieldGuard) {
	if g.isPointer {
		fmt.Fprintf(buf, "\tif s.%s == nil {\n", g.name)
	} else {
		fmt.Fprintf(buf, "\tif validation.IsNilInterface(s.%s) {\n", g.name)
	}
	fmt.Fprintf(buf, "\t\treturn errcode.New(%s, %s,\n", g.kindExpr, g.codeExpr)
	fmt.Fprintf(buf, "\t\t\t%s)\n", strconv.Quote(g.errMsg))
	buf.WriteString("\t}\n")
}

// FindSlicePaths discovers all slice directories containing a service.go under
// modRoot. Exported so the REQUIRED-DEP-NIL-GUARD-01 A1 archtest regen-diffs the
// exact same set of gen files the generator emits — generator and verifier share
// one discovery, so no gen file can escape the byte-granularity Hard check.
func FindSlicePaths(modRoot string) ([]string, error) {
	patterns := []string{
		filepath.Join(modRoot, "cells", "*", "slices", "*"),
		filepath.Join(modRoot, "cells", "*", "slices", "*", "*"),
		filepath.Join(modRoot, "cells", "*", "internal", "*"),
		// corecells is GoCell's dedicated platform-cell module (#1560) with a
		// FLAT layout — cells sit directly under the module root, e.g.
		// corecells/accesscore/{slices,internal}/<x>/service.go — so it needs
		// its own no-"cells"-infix patterns. modRoot is the repo root (the
		// workspace root from findRoot), under which corecells/ is a physical
		// subtree, so these glob the corecells gen files the same as cells/.
		filepath.Join(modRoot, "corecells", "*", "slices", "*"),
		filepath.Join(modRoot, "corecells", "*", "slices", "*", "*"),
		filepath.Join(modRoot, "corecells", "*", "internal", "*"),
		filepath.Join(modRoot, "examples", "*", "cells", "*", "slices", "*"),
		filepath.Join(modRoot, "examples", "*", "cells", "*", "internal", "*"),
	}

	seen := make(map[string]struct{})
	var result []string

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, dir := range matches {
			// Skip non-directories (e.g. service.go matched by a wildcard).
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				continue
			}
			// Include only directories that contain a service.go.
			svcFile := filepath.Join(dir, "service.go")
			if _, err := os.Stat(svcFile); err != nil {
				continue
			}
			if _, ok := seen[svcFile]; ok {
				continue
			}
			seen[svcFile] = struct{}{}
			result = append(result, dir)
		}
	}
	return result, nil
}
