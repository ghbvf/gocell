// SPAN-RECORD-ERROR-REDACT-01 — every span.RecordError(...) call inside
// kernel/wrapper/ and runtime/http/middleware/ must wrap its argument with
// pkg/redaction.RedactError(...). Hardcoded fail-closed redaction has no
// caller-side opt-out (see ADR §8); this gate ensures future additions of
// `RecordError` do not silently bypass the redactor.
//
// Detection (pure AST, no go/types — scope is two directories, three known
// call sites; loader cost is unwarranted):
//  1. Walk every non-test .go file in the two directories.
//  2. For each file, locate the import path `github.com/ghbvf/gocell/pkg/redaction`
//     and record its local name (default `redaction`, or alias).
//  3. Find every `*ast.CallExpr` whose Fun is a `*ast.SelectorExpr` with
//     `Sel.Name == "RecordError"`.
//  4. Assert the first argument is a `*ast.CallExpr` whose Fun is a
//     `*ast.SelectorExpr` with `X.(*ast.Ident).Name == <redaction local name>`
//     and `Sel.Name == "RedactError"`.
//
// Test files are skipped — spy/mock spans in *_test.go intentionally inspect
// raw error values and have no observability surface.
//
// ref: ADR docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md §8
// ref: docs/backlog1.md §2.1 SPAN-RECORD-ERROR-REDACT-ARCHTEST-01
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

const redactionImportPath = `"github.com/ghbvf/gocell/pkg/redaction"`

// spanRecordErrorScanDirs lists the directories whose non-test .go files
// must route every RecordError call through redaction.RedactError.
//
// New directories should be added here whenever a new package starts
// emitting span.RecordError on a production code path.
var spanRecordErrorScanDirs = []string{
	"kernel/wrapper",
	"runtime/http/middleware",
}

// redactionLocalName returns the local identifier used in file to refer to
// the redaction package (default "redaction" for an unnamed import; alias
// otherwise). Returns "" when the file does not import pkg/redaction at all
// — in that case any RecordError call is automatically a violation, since
// it cannot possibly invoke redaction.RedactError.
func redactionLocalName(file *ast.File) string {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		if imp.Path.Value != redactionImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "redaction"
	}
	return ""
}

// isRedactErrorCall reports whether expr is a call of the form
// `<redactionLocal>.RedactError(...)`.
func isRedactErrorCall(expr ast.Expr, redactionLocal string) bool {
	if redactionLocal == "" {
		return false
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel == nil || sel.Sel.Name != "RedactError" {
		return false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return xIdent.Name == redactionLocal
}

// scanSpanRecordErrorFile walks file and reports every `*.RecordError(...)`
// call whose first argument is not `<redaction>.RedactError(...)`.
func scanSpanRecordErrorFile(fset *token.FileSet, file *ast.File, rel string) []string {
	redactionLocal := redactionLocalName(file)

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
			return true
		}
		// Bare `RecordError()` with no arg — only legal in tests; in prod
		// code this is structurally wrong (RecordError requires error arg).
		if len(call.Args) == 0 {
			return true
		}
		if isRedactErrorCall(call.Args[0], redactionLocal) {
			return true
		}
		line := fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: span.RecordError(...) first arg must be redaction.RedactError(...) "+
				"— hardcoded fail-closed redaction has no caller-side opt-out (ADR §8)",
			rel, line))
		return true
	})
	return out
}

// scanSpanRecordErrorDir walks every non-test .go file under root/dir and
// returns SPAN-RECORD-ERROR-REDACT-01 violations.
func scanSpanRecordErrorDir(t *testing.T, root, dir string) []string {
	t.Helper()
	abs := filepath.Join(root, dir)
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
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		out = append(out, scanSpanRecordErrorFile(fset, file, rel)...)
		return nil
	})
	require.NoError(t, err, "walk %s", abs)
	return out
}

// TestSpanRecordErrorRedacted enforces SPAN-RECORD-ERROR-REDACT-01.
func TestSpanRecordErrorRedacted(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	var violations []string
	for _, dir := range spanRecordErrorScanDirs {
		violations = append(violations, scanSpanRecordErrorDir(t, root, dir)...)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"SPAN-RECORD-ERROR-REDACT-01: every span.RecordError(...) call in "+
			"kernel/wrapper/ and runtime/http/middleware/ must wrap its first "+
			"argument with pkg/redaction.RedactError(...). "+
			"ref: docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md §8")
}

// runSpanRecordErrorFixtureScan parses fixture .go files (non-test, no module
// load) and reports violations relative to fixtureDir.
func runSpanRecordErrorFixtureScan(t *testing.T, fixtureDir string) []string {
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
		out = append(out, scanSpanRecordErrorFile(fset, file, rel)...)
		return nil
	})
	require.NoError(t, err, "walk fixture %s", fixtureDir)
	sort.Strings(out)
	return out
}

// TestSpanRecordErrorRedactedFixtures verifies the AST scanner via static
// regression cases (compliant: 0 violations, violates: 1 violation).
func TestSpanRecordErrorRedactedFixtures(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "span_record_error_fixtures")

	cases := []struct {
		pkg           string
		wantViolCount int
	}{
		{"compliant", 0},
		{"violates", 1},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			got := runSpanRecordErrorFixtureScan(t, filepath.Join(base, tc.pkg))
			assert.Equal(t, tc.wantViolCount, len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.pkg, tc.wantViolCount, len(got), got)
		})
	}
}
