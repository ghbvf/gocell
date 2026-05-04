// NO-DELETED-AUTH-SYMBOLS-01
//
// Invariant: no production or test .go file (outside the canonical
// definition site) may reference:
//   - auth.RoleInternalAdmin
//   - auth.ServiceNameInternal
//   - auth.BuiltinServiceRoles
//
// These symbols are being removed as part of Wave 2 of the 4-part service
// token caller-identity migration (PR A5 SVCTOKEN-CALLER-IDENTITY). Once
// production code no longer uses them, they will be deleted from
// runtime/auth/principal.go.
//
// Whitelist: runtime/auth/principal.go itself is the canonical definition
// site and is exempt during Wave 1 (the symbols still exist there). After
// Wave 2 deletes them from principal.go, the whitelist entry is removed
// and this gate becomes a pure "0 references" check.
//
// Detection: AST-level selector expression scan — `auth.RoleInternalAdmin`,
// `auth.ServiceNameInternal`, `auth.BuiltinServiceRoles` — in all .go files
// under runtime/, cells/, cmd/, kernel/, adapters/, examples/, tests/.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const ruleNoDeletedAuthSymbols01 = "NO-DELETED-AUTH-SYMBOLS-01"

// deletedAuthSymbols is the set of selector names that must not appear in
// any file outside the whitelist.
var deletedAuthSymbols = map[string]bool{
	"RoleInternalAdmin":   true,
	"ServiceNameInternal": true,
	"BuiltinServiceRoles": true,
}

// deletedAuthSymbolsAllowlist contains module-relative paths that are exempt
// from NO-DELETED-AUTH-SYMBOLS-01. During Wave 1 the definition file is
// allowed; once Wave 2 removes the definitions, this allowlist becomes empty.
var deletedAuthSymbolsAllowlist = map[string]bool{
	// Wave 1: principal.go still defines these symbols — allow the definition site.
	"runtime/auth/principal.go": true,
}

// TestNO_DELETED_AUTH_SYMBOLS_01 enforces that the deprecated
// auth.RoleInternalAdmin / auth.ServiceNameInternal / auth.BuiltinServiceRoles
// symbols are not referenced anywhere outside their canonical definition site.
//
// Note: this test FAILS (RED) until Wave 2 removes all references from
// production code and test files that currently use these symbols.
func TestNO_DELETED_AUTH_SYMBOLS_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cells"),
		filepath.Join(root, "cmd"),
		filepath.Join(root, "kernel"),
		filepath.Join(root, "adapters"),
		filepath.Join(root, "examples"),
		filepath.Join(root, "tests"),
	}

	var violations []string

	for _, dir := range searchDirs {
		allFiles, err := findAllGoFilesInDir(dir)
		if err != nil {
			continue
		}
		for _, f := range allFiles {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)

			if deletedAuthSymbolsAllowlist[rel] {
				continue
			}

			hits, scanErr := scanDeletedAuthSymbols(f, rel)
			if scanErr != nil {
				t.Logf("scan error %s: %v", rel, scanErr)
				continue
			}
			violations = append(violations, hits...)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	if len(violations) > 0 {
		t.Errorf("%s: %d references to deprecated auth symbols found.\n"+
			"auth.RoleInternalAdmin / auth.ServiceNameInternal / auth.BuiltinServiceRoles\n"+
			"are being deleted in Wave 2. Replace with auth.RequireCallerCell or\n"+
			"auth.TestServiceContext. See PR A5 SVCTOKEN-CALLER-IDENTITY.",
			ruleNoDeletedAuthSymbols01, len(violations))
	}
}

// scanDeletedAuthSymbols parses a single .go file and returns violation
// strings for every `auth.RoleInternalAdmin`, `auth.ServiceNameInternal`,
// or `auth.BuiltinServiceRoles` selector expression.
func scanDeletedAuthSymbols(path, rel string) ([]string, error) {
	data, err := readGoFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil //nolint:nilerr // soft-skip on read error: archtest fixture allows missing/unreadable files (caller will scan rest)
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if !deletedAuthSymbols[sel.Sel.Name] {
			return true
		}
		// Only flag auth.X (receiver named "auth").
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "auth" {
			return true
		}
		pos := fset.Position(sel.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: deprecated symbol auth.%s — replace with caller-cell identity pattern",
			rel, pos.Line, sel.Sel.Name))
		return true
	})

	// Also scan for bare references (e.g. inside the auth package itself where
	// the qualifier is omitted). In non-auth packages, the selector form is
	// sufficient; in auth package tests, unqualified references would look like
	// plain Ident nodes.
	//
	// For the runtime/auth package tests, check Ident nodes too.
	if strings.Contains(rel, "runtime/auth/") {
		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if !deletedAuthSymbols[ident.Name] {
				return true
			}
			// Exclude the selector's Sel field — already caught above.
			pos := fset.Position(ident.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: deprecated symbol %s — replace with caller-cell identity pattern",
				rel, pos.Line, ident.Name))
			return true
		})
	}

	return violations, nil
}

// readGoFile reads a .go file, returning its bytes.
func readGoFile(path string) ([]byte, error) {
	return os.ReadFile(filepath.Clean(path))
}
