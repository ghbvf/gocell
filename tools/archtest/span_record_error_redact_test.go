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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const redactionImportPath = `"github.com/ghbvf/gocell/pkg/redaction"`

// spanRedactViolMsg is the diagnostic message emitted when a RecordError call
// does not wrap its argument with redaction.RedactError.
//
// Serialization protocol — two paths share this constant with different
// surrounding context:
//
//  1. Fixture scanner (scanSpanRecordErrorFile): emits position-prefixed
//     strings of the form "<rel>:<line>: <spanRedactViolMsg>", used only in
//     test assertions within TestSpanRecordErrorRedactedFixtures.
//
//  2. Production enforcement (spanRecordErrorDirDiags): populates
//     Diagnostic.Message directly (no prefix); the Diagnostic struct carries
//     Rel and Line as separate fields, and Report formats them independently.
//
// Callers must not strip or reformat the constant value; add position context
// via the Diagnostic struct rather than by mutating the message text.
const spanRedactViolMsg = "span.RecordError(...) first arg must be redaction.RedactError(...)" +
	" — hardcoded fail-closed redaction has no caller-side opt-out (ADR §8)"

// spanCoverageViolMsg is the diagnostic message emitted when a file calls
// RecordError but its directory is not enrolled in spanRecordErrorScanDirs.
const spanCoverageViolMsg = "production file calls span.RecordError(...) but its directory" +
	" is not enrolled in spanRecordErrorScanDirs — add the directory or relocate the call"

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
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
			return
		}
		if posInRanges(call.Pos(), implRanges) {
			return
		}
		found = true
	})
	return found
}

// collectRecordErrorImplRanges returns the body Pos/End pairs of every
// `RecordError` method declaration in file. Pairs are flat: even indices
// are start positions, odd indices are end positions.
func collectRecordErrorImplRanges(file *ast.File) []token.Pos {
	var ranges []token.Pos
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Recv == nil || fn.Name == nil {
			return
		}
		if fn.Name.Name != "RecordError" {
			return
		}
		ranges = append(ranges, fn.Body.Lbrace, fn.Body.Rbrace)
	})
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
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
			return
		}
		// Bare `RecordError()` with no arg — only legal in tests; in prod
		// code this is structurally wrong (RecordError requires error arg).
		if len(call.Args) == 0 {
			return
		}
		if isRedactErrorCall(call.Args[0], redactionLocal) {
			return
		}
		line := fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf("%s:%d: %s", rel, line, spanRedactViolMsg))
	})
	return out
}

// spanRecordErrorDirDiags returns SPAN-RECORD-ERROR-REDACT-01 Diagnostics for dir.
//
// IncludeGenerated mirrors the option used by TestSpanRecordErrorScanDirsCoverage
// so that a generated/ directory enrolled in spanRecordErrorScanDirs (for
// instance generated/contracts emitting span.RecordError from handler_gen.go)
// is actually enforced, not just covered.
func spanRecordErrorDirDiags(t *testing.T, root, dir string) []Diagnostic {
	t.Helper()
	scope := DirsScope(root, []string{dir}, IncludeGenerated())
	return Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			redactionLocal := redactionLocalName(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
					return
				}
				if len(call.Args) == 0 {
					return
				}
				if isRedactErrorCall(call.Args[0], redactionLocal) {
					return
				}
				pos := p.Fset.Position(call.Pos())
				ds = append(ds, Diagnostic{
					Rel:     p.Rel(file),
					Line:    pos.Line,
					Message: spanRedactViolMsg,
				})
			})
		}
		return ds
	})
}

// TestSpanRecordErrorRedacted enforces SPAN-RECORD-ERROR-REDACT-01.
func TestSpanRecordErrorRedacted(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	var allDiags []Diagnostic
	for _, dir := range spanRecordErrorScanDirs {
		allDiags = append(allDiags, spanRecordErrorDirDiags(t, root, dir)...)
	}
	Report(t, "SPAN-RECORD-ERROR-REDACT-01", allDiags)
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

	enrolledDirs := make(map[string]struct{}, len(spanRecordErrorScanDirs))
	for _, dir := range spanRecordErrorScanDirs {
		enrolledDirs[filepath.Clean(dir)] = struct{}{}
	}

	// IncludeGenerated: SPAN-RECORD-ERROR-REDACT-01 covers every production
	// .go file in the repo, including codegen output (handler_gen.go and
	// similar may emit span.RecordError). Without this option ModuleScope's
	// default skip set would silently shrink the coverage gate.
	scope := ModuleScope(root, IncludeGenerated())
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			if !fileHasNonImplRecordError(file) {
				continue
			}
			rel := filepath.ToSlash(p.Rel(file))
			dir := filepath.Dir(rel)
			enrolled := false
			for d := dir; d != "." && d != "/"; d = filepath.Dir(d) {
				if _, ok := enrolledDirs[filepath.Clean(d)]; ok {
					enrolled = true
					break
				}
			}
			if !enrolled {
				ds = append(ds, Diagnostic{
					Rel:     rel,
					Line:    0,
					Message: spanCoverageViolMsg,
				})
			}
		}
		return ds
	})
	Report(t, "SPAN-RECORD-ERROR-REDACT-01-COVERAGE", diags)
}

// runSpanRecordErrorFixtureScan parses fixture .go files and reports
// violations relative to fixtureDir. Uses DirsScope+IncludeTestdata to
// funnel through the framework even though fixtures live under testdata/
// (the default skip set excludes testdata; IncludeTestdata is the authorized
// opt-in).
//
// This is a fixture-only helper — it does not follow the production Diagnostic
// path. Run(...) is invoked solely to walk p.Files via the framework; the
// closure always returns nil (no Diagnostics). Violations are instead
// accumulated into the outer `out []string` slice by scanSpanRecordErrorFile,
// which returns position-prefixed strings rather than Diagnostic structs.
// DirsScope without IncludeTests() never includes *_test.go files, so no
// explicit test-file guard is needed here.
//
// IncludeGenerated mirrors the option used by the production enforcement walk
// spanRecordErrorDirDiags, so the violates_in_generated fixture (which buries
// the offending file under a "generated" subdirectory) actually reaches the
// scanner. Without it the default skip set drops the file and the fixture
// silently passes — making the production IncludeGenerated() a no-op.
//
// fixtureDirRel is the module-relative slash path to the fixture directory.
func runSpanRecordErrorFixtureScan(t *testing.T, root, fixtureDirRel string) []string {
	t.Helper()
	scope := DirsScope(root, []string{fixtureDirRel}, IncludeTestdata(), IncludeGenerated())
	var out []string
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			out = append(out, scanSpanRecordErrorFile(p.Fset, file, filepath.Base(p.Abs(file)))...)
		}
		return nil
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
		// violates_in_generated buries the offending file under a "generated"
		// subdirectory; reaching it requires runSpanRecordErrorFixtureScan to
		// pass IncludeGenerated() to the scope. Removing that option from the
		// fixture scan flips wantViolCount=1 to observed=0 and turns red.
		{"violates_in_generated", 1},
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
