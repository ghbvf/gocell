// Package requireddepsgen generates validateRequired() methods for Service
// structs by reading gocell:"required" struct field tags.
//
// Usage:
//
//	out, err := requireddepsgen.Generate("/path/to/slice")
//	// out contains the formatted Go source for service_required_gen.go
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
	"strings"
)

// Sentinel errors returned by Generate.
var (
	// ErrNoServiceStruct is returned when service.go contains no
	// "type Service struct" declaration.
	ErrNoServiceStruct = errors.New("requireddepsgen: no Service struct in service.go")

	// ErrUnknownTagValue is returned when a struct field has a gocell tag
	// whose value is not "" or "required".
	ErrUnknownTagValue = errors.New("requireddepsgen: unknown gocell tag value (allowed: \"\", \"required\")")

	// ErrBadTagSyntax is returned when a struct field tag is malformed and
	// cannot be parsed by reflect.StructTag.
	ErrBadTagSyntax = errors.New("requireddepsgen: malformed struct tag")
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
	slicePaths, err := findSlicePaths(modRoot)
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
			g := buildGuard(pkgName, ident.Name, field.Type, tag)
			guards = append(guards, g)
		}
	}
	return guards, nil
}

// buildGuard constructs the fieldGuard for a single named field.
func buildGuard(pkgName, fieldName string, fieldType ast.Expr, tag reflect.StructTag) fieldGuard {
	isPtr := isPointerType(fieldType)

	kind := resolveTagOrDefault(tag.Get("gocellKind"), defaultKind, "errcode.")
	code := resolveTagOrDefault(tag.Get("gocellCode"), defaultCode, "errcode.")
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
	}
}

// isPointerType reports whether the AST type expression is a pointer (*T).
// SelectorExpr (pkg.Type) and Ident are treated as interface types.
func isPointerType(expr ast.Expr) bool {
	_, ok := expr.(*ast.StarExpr)
	return ok
}

// resolveTagOrDefault qualifies a bare identifier with prefix if not already
// qualified, falling back to def when val is empty.
func resolveTagOrDefault(val, def, prefix string) string {
	if val == "" {
		return def
	}
	if strings.HasPrefix(val, prefix) {
		return val
	}
	return prefix + val
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
func writeGuard(buf *bytes.Buffer, g fieldGuard) {
	if g.isPointer {
		fmt.Fprintf(buf, "\tif s.%s == nil {\n", g.name)
	} else {
		fmt.Fprintf(buf, "\tif validation.IsNilInterface(s.%s) {\n", g.name)
	}
	fmt.Fprintf(buf, "\t\treturn errcode.New(%s, %s,\n", g.kindExpr, g.codeExpr)
	fmt.Fprintf(buf, "\t\t\t\"%s\")\n", g.errMsg)
	buf.WriteString("\t}\n")
}

// findSlicePaths discovers all slice directories containing a service.go under modRoot.
func findSlicePaths(modRoot string) ([]string, error) {
	patterns := []string{
		filepath.Join(modRoot, "cells", "*", "slices", "*"),
		filepath.Join(modRoot, "cells", "*", "slices", "*", "*"),
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
