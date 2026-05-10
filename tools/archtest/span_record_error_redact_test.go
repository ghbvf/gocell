// invariants:
//   - INVARIANT: SPAN-RECORD-ERROR-REDACT-01
//   - INVARIANT: SPAN-RECORD-ERROR-REDACT-ARCHTEST-01
//
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
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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

// fileHasNonImplRecordError reports whether file contains at least one
// `*.RecordError(...)` call that is NOT inside the body of a `RecordError`
// method declaration. Method-scope (not file-scope) skip: span-impl files
// like adapters/otel/span.go declare RecordError as a forwarder; calls
// inside that body legitimately delegate to the inner library span and
// must not trigger the enrollment gate. Any RecordError call ELSEWHERE in
// the same file (including file-level expressions) is still subject to
// the gate, so a span-impl file that grows a second non-impl call site
// cannot bypass coverage.
//
// ref: golang/tools go/analysis — typed scope tracking via parent funcs
func fileHasNonImplRecordError(file *ast.File) bool {
	implRanges := collectRecordErrorImplRanges(file)
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
			return true
		}
		if posInRanges(call.Pos(), implRanges) {
			return true
		}
		found = true
		return false
	})
	return found
}

// collectRecordErrorImplRanges returns the body Pos/End pairs of every
// `RecordError` method declaration in file. Pairs are flat: even indices
// are start positions, odd indices are end positions.
func collectRecordErrorImplRanges(file *ast.File) []token.Pos {
	var ranges []token.Pos
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Recv == nil || fn.Name == nil {
			continue
		}
		if fn.Name.Name != "RecordError" {
			continue
		}
		ranges = append(ranges, fn.Body.Lbrace, fn.Body.Rbrace)
	}
	return ranges
}

func posInRanges(p token.Pos, ranges []token.Pos) bool {
	for i := 0; i+1 < len(ranges); i += 2 {
		if p >= ranges[i] && p <= ranges[i+1] {
			return true
		}
	}
	return false
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

// scanSpanRecordErrorDir scans every non-test .go file under root/dir and
// returns SPAN-RECORD-ERROR-REDACT-01 violations.
//
// IncludeGenerated mirrors the option used by TestSpanRecordErrorScanDirsCoverage
// so that a generated/ directory enrolled in spanRecordErrorScanDirs (for
// instance generated/contracts emitting span.RecordError from handler_gen.go)
// is actually enforced, not just covered.
func scanSpanRecordErrorDir(t *testing.T, root, dir string) []string {
	t.Helper()
	scope := scanner.DirsScope(root, []string{dir}, scanner.IncludeGenerated())
	var out []string
	scanner.EachFile(t, scope, parser.ParseComments, func(t *testing.T, fc scanner.FileContext) {
		out = append(out, scanSpanRecordErrorFile(fc.Fset, fc.File, fc.Rel)...)
	})
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

// TestSpanRecordErrorScanDirsCoverage is the fail-closed coverage gate for
// SPAN-RECORD-ERROR-REDACT-01: it scans every production .go file in the repo
// for any `*.RecordError(...)` call and asserts the file's directory tree is
// already enrolled in spanRecordErrorScanDirs. Without this, a new package
// emitting span.RecordError would silently bypass the redaction guard until
// someone manually appended its directory.
func TestSpanRecordErrorScanDirsCoverage(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	enrolled := make(map[string]struct{}, len(spanRecordErrorScanDirs))
	for _, dir := range spanRecordErrorScanDirs {
		enrolled[filepath.Clean(dir)] = struct{}{}
	}

	var unenrolled []string
	// IncludeGenerated: SPAN-RECORD-ERROR-REDACT-01 covers every production
	// .go file in the repo, including codegen output (handler_gen.go and
	// similar may emit span.RecordError). Without this option ModuleScope's
	// default skip set would silently shrink the coverage gate.
	scope := scanner.ModuleScope(root, scanner.IncludeGenerated())
	scanner.EachFile(t, scope, parser.ParseComments, func(t *testing.T, fc scanner.FileContext) {
		if !fileHasNonImplRecordError(fc.File) {
			return
		}
		rel := filepath.ToSlash(fc.Rel)
		dir := filepath.Dir(rel)
		// Walk up looking for any enrolled prefix.
		for d := dir; d != "." && d != "/"; d = filepath.Dir(d) {
			if _, ok := enrolled[filepath.Clean(d)]; ok {
				return
			}
		}
		unenrolled = append(unenrolled, rel)
	})
	sort.Strings(unenrolled)
	assert.Empty(t, unenrolled,
		"SPAN-RECORD-ERROR-REDACT-01 coverage: production files calling "+
			"span.RecordError(...) must live under a directory enrolled in "+
			"spanRecordErrorScanDirs. New offenders found above — either add "+
			"the directory to the enrollment list or relocate the call.")
}

// runSpanRecordErrorFixtureScan parses fixture .go files (non-test) and reports
// violations relative to fixtureDir. Uses scanner.DirsScope+IncludeTestdata to
// funnel through the framework even though fixtures live under testdata/
// (the default skip set excludes testdata; IncludeTestdata is the authorized
// opt-in).
//
// fixtureDirRel is the module-relative slash path to the fixture directory.
func runSpanRecordErrorFixtureScan(t *testing.T, root, fixtureDirRel string) []string {
	t.Helper()
	scope := scanner.DirsScope(root, []string{fixtureDirRel}, scanner.IncludeTestdata())
	var out []string
	scanner.EachFile(t, scope, parser.ParseComments, func(_ *testing.T, fc scanner.FileContext) {
		if strings.HasSuffix(fc.AbsPath, "_test.go") {
			return
		}
		out = append(out, scanSpanRecordErrorFile(fc.Fset, fc.File, filepath.Base(fc.AbsPath))...)
	})
	sort.Strings(out)
	return out
}

// TestSpanRecordErrorRedactedFixtures verifies the AST scanner via static
// regression cases (compliant: 0 violations, violates: 1 violation).
func TestSpanRecordErrorRedactedFixtures(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	baseRel := "tools/archtest/testdata/span_record_error_fixtures"

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
			got := runSpanRecordErrorFixtureScan(t, root, baseRel+"/"+tc.pkg)
			assert.Equal(t, tc.wantViolCount, len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.pkg, tc.wantViolCount, len(got), got)
		})
	}
}

// TestSpanRecordErrorMethodScopeSkip is the reverse-fixture for the
// method-scope skip in TestSpanRecordErrorScanDirsCoverage. It proves that
// a file which simultaneously (a) declares a `RecordError` method and (b)
// invokes `*.RecordError(...)` from another function still triggers the
// coverage gate via fileHasNonImplRecordError. Without the method-scope
// fix, the whole file was skipped and (b) silently bypassed enrollment.
//
// Inline-source so we do not need to add a violator to production tree.
//
// ref: rust-lang/rust-clippy `expect` mechanism for catching missing-lint
// ref: cockroachdb/cockroach gcassert inventory completeness check
func TestSpanRecordErrorMethodScopeSkip(t *testing.T) {
	t.Parallel()

	srcs := map[string]struct {
		src       string
		wantFound bool
	}{
		"impl_only_no_other_call": {
			src: `package x
type span struct{}
func (s *span) RecordError(err error) {
	s.inner.RecordError(err)
}
`,
			wantFound: false,
		},
		"impl_plus_unenrolled_caller": {
			// span impl + bypass call site in another function
			src: `package x
type span struct{}
func (s *span) RecordError(err error) {
	s.inner.RecordError(err)
}
type Span interface{ RecordError(error) }
func leaks(s Span, err error) {
	s.RecordError(err)
}
`,
			wantFound: true,
		},
		"file_level_var_init_caller": {
			// non-impl file-level call (legal in var initializer) must flag
			src: `package x
type Span interface{ RecordError(error) }
var noop = func(s Span, err error) { s.RecordError(err) }
`,
			wantFound: true,
		},
	}

	for name, tc := range srcs {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, name+".go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "fixture must parse")
			got := fileHasNonImplRecordError(file)
			assert.Equal(t, tc.wantFound, got,
				"fileHasNonImplRecordError(%s) = %v; want %v (method-scope must NOT skip non-impl callers)",
				name, got, tc.wantFound)
		})
	}
}
