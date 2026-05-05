// DETAILS-SLOG-ATTR-01 — every call to `errcode.WithDetails(...)` in
// production code must pass typed slog.Attr arguments, not the legacy
// `map[string]any{...}` literal form. The signature change is a hard cutover
// (see ADR docs/architecture/202605051730-adr-errcode-message-pii-safety.md);
// this archtest prevents regression by flagging map-literal arguments at
// build time.
//
// Detection (pure AST, no go/types — scope is small and the violation
// pattern is structurally distinct):
//   1. Walk every non-test .go file in production directories (kernel/,
//      runtime/, adapters/, cells/, cmd/, examples/, pkg/, tools/).
//   2. Find every *ast.CallExpr whose Fun resolves to a selector with
//      Sel.Name == "WithDetails" and X (Ident).Name in the set of known
//      local names for the errcode package import (default "errcode" or any
//      alias in scope).
//   3. For each WithDetails call, inspect each Arg: if any Arg is a
//      *ast.CompositeLit whose Type is a *ast.MapType (map literal) the
//      call is a violation.
//
// Test files are skipped (they may legitimately exercise the legacy form
// during migration cleanup, and tests do not ship to production).
//
// Allow-list:
//   - pkg/errcode/ (the migration target itself)
//   - tools/archtest/testdata/ (fixture violations are intentional)
//
// ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleDetailsSlogAttr01 = "DETAILS-SLOG-ATTR-01"

// errcodeImportPathLit is the quoted import path emitted by the parser in
// ast.ImportSpec.Path.Value (literal form, including the surrounding
// double quotes). Distinct from errcodeImportPath in
// errcode_constructor_test.go which stores the unquoted form for
// strconv.Unquote-based comparison.
const errcodeImportPathLit = `"github.com/ghbvf/gocell/pkg/errcode"`

// detailsSlogAttrScanRoots are the top-level directories whose non-test .go
// files are scanned. Adding a new top-level directory under module root
// requires explicit registration here.
var detailsSlogAttrScanRoots = []string{
	"adapters",
	"cells",
	"cmd",
	"examples",
	"kernel",
	"pkg",
	"runtime",
	"tools",
}

// detailsSlogAttrAllowlist lists path prefixes that are exempt from the
// gate. Entries are matched against the module-relative path.
var detailsSlogAttrAllowlist = []string{
	"pkg/errcode/",
	"tools/archtest/testdata/",
}

// errcodeLocalName returns the local identifier used in file to refer to
// pkg/errcode (default "errcode" for an unnamed import; alias otherwise).
// Returns "" when the file does not import errcode at all — in that case
// any "WithDetails" selector cannot resolve to errcode.WithDetails.
func errcodeLocalName(file *ast.File) string {
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != errcodeImportPathLit {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "errcode"
	}
	return ""
}

// argHasMapLiteral reports whether expr is or contains a *ast.CompositeLit
// whose Type is a *ast.MapType (excluding struct/slice composite literals).
// We only flag the outermost arg shape; nested map literals inside a typed
// slog.Group / slog.Any are caller-controlled and out of scope.
func argHasMapLiteral(expr ast.Expr) bool {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	_, isMap := cl.Type.(*ast.MapType)
	return isMap
}

// scanWithDetailsFile walks file and reports every
// `<errcodeLocal>.WithDetails(map[...]{...})` call whose argument is a map
// literal.
func scanWithDetailsFile(fset *token.FileSet, file *ast.File, rel string) []string {
	local := errcodeLocalName(file)
	if local == "" {
		return nil
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "WithDetails" {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != local {
			return true
		}
		for _, arg := range call.Args {
			if argHasMapLiteral(arg) {
				line := fset.Position(call.Pos()).Line
				out = append(out, fmt.Sprintf(
					"%s:%d: errcode.WithDetails(map[string]any{...}) — pass typed slog.Attr "+
						"values instead. ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md",
					rel, line))
				continue
			}
			if name, ok := unsafeSlogAttrConstructor(arg); ok {
				line := fset.Position(call.Pos()).Line
				out = append(out, fmt.Sprintf(
					"%s:%d: errcode.WithDetails(slog.%s(...)) — wire-unsafe kind; "+
						"use scalar slog.String/Int/Uint64/Float64/Bool/Duration/Time. "+
						"ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md",
					rel, line, name))
			}
		}
		return true
	})
	return out
}

// unsafeSlogAttrConstructor reports whether expr is a slog constructor whose
// resulting Attr.Value carries a wire-unsafe kind (KindAny / KindGroup /
// KindLogValuer). The detection is purely syntactic — selector match on
// "slog.Any" / "slog.Group" / "slog.LogValue" — to keep this archtest free
// of go/types loads.
func unsafeSlogAttrConstructor(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.X == nil || sel.Sel == nil {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "slog" {
		return "", false
	}
	switch sel.Sel.Name {
	case "Any", "Group", "LogValue":
		return sel.Sel.Name, true
	}
	return "", false
}

// scanWithDetailsDir walks every non-test .go file under root/dir and
// returns DETAILS-SLOG-ATTR-01 violations.
func scanWithDetailsDir(t *testing.T, root, dir string) []string {
	t.Helper()
	abs := filepath.Join(root, dir)
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return nil
	}
	var out []string
	err := filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, prefix := range detailsSlogAttrAllowlist {
			if strings.HasPrefix(rel, prefix) {
				return nil
			}
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		out = append(out, scanWithDetailsFile(fset, file, rel)...)
		return nil
	})
	require.NoError(t, err, "walk %s", abs)
	return out
}

// TestDetailsSlogAttr enforces DETAILS-SLOG-ATTR-01 across production code.
func TestDetailsSlogAttr(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	var violations []string
	for _, dir := range detailsSlogAttrScanRoots {
		violations = append(violations, scanWithDetailsDir(t, root, dir)...)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"%s: errcode.WithDetails must receive typed slog.Attr arguments. "+
			"ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md",
		ruleDetailsSlogAttr01)
}

// runDetailsSlogAttrFixtureScan parses fixture .go files (non-test, no
// module load) and reports violations relative to fixtureDir.
func runDetailsSlogAttrFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(fixtureDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(fixtureDir, path)
		if relErr != nil {
			rel = path
		}
		out = append(out, scanWithDetailsFile(fset, file, rel)...)
		return nil
	})
	require.NoError(t, err, "walk fixture %s", fixtureDir)
	sort.Strings(out)
	return out
}

// TestDetailsSlogAttrFixtures verifies the AST scanner via static
// regression cases.
func TestDetailsSlogAttrFixtures(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "details_slog_attr")

	cases := []struct {
		pkg           string
		wantViolCount int
	}{
		{"compliant", 0},
		{"violates", 3}, // map literal + slog.Any + slog.Group
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			got := runDetailsSlogAttrFixtureScan(t, filepath.Join(base, tc.pkg))
			assert.Equal(t, tc.wantViolCount, len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.pkg, tc.wantViolCount, len(got), got)
		})
	}
}
