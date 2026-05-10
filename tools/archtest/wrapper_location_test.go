// invariants:
//   - INVARIANT: CELL-RAW-INFRA-WRAPPER-LOCATION-01
//
// CELL-RAW-INFRA-WRAPPER-LOCATION-01 — persistence.WrapForCell /
// outbox.WrapPublisherForCell / outbox.WrapWriterForCell are the sole
// authorized paths for handing raw infra types into a cell's With* Option.
// They MUST be called only from composition roots (cmd/* +
// examples/<demo>/main.go + examples/<demo>/app.go) or *_test.go. Any other
// caller — most importantly a cell package — risks recreating the bypass
// that the sealed marker (kernel/persistence.CellTxManager,
// kernel/outbox.CellPublisher / CellWriter) eliminated.
//
// AI-rebust 评级：Medium (archtest type-aware via typeseval.SharedResolver
// caller-package check). The sealed marker is the AI-HARD primary defense
// (违反不可表达 — cells can no longer declare With* Options that accept raw
// types because the type system rejects raw → CellXxx assignment without
// the wrapper). This archtest is belt-and-suspenders that additionally pins
// the authorized wrap-call locations, preventing a cell from importing
// kernel/persistence and calling WrapForCell internally.
//
// ref: docs/architecture/<adr-cell-raw-infra-sealed-marker>.md §D2
// ref: ADR 202605101800 §D6 (history; archtest scanner predecessor deleted)
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// wrapperFunctionsCanonical is the closed set of wrapper functions whose
// callers are restricted by CELL-RAW-INFRA-WRAPPER-LOCATION-01. Adding a
// new wrapper requires updating this set AND wrapperLocationAllowlistDoc
// (godoc above) so the rule's surface stays trivially auditable.
var wrapperFunctionsCanonical = map[string]bool{
	"github.com/ghbvf/gocell/kernel/persistence.WrapForCell":     true,
	"github.com/ghbvf/gocell/kernel/outbox.WrapPublisherForCell": true,
	"github.com/ghbvf/gocell/kernel/outbox.WrapWriterForCell":    true,
}

type wrapperViolation struct {
	File     string
	Line     int
	FuncName string
}

// isWrapperCallerAllowed reports whether rel (slash-separated, relative to
// module root) is an authorized site for wrapper-function calls.
//
// Allowed:
//   - Any *_test.go (tests construct fakes / drive integration scenarios)
//   - Any file under cmd/ (composition root)
//   - examples/<demo>/main.go and examples/<demo>/app.go (composition root)
//   - kernel/persistence/cell_marker.go and kernel/outbox/cell_marker.go
//     (the wrapper definitions themselves)
//   - kernel/cell/demo_tx_runner.go (DemoCellTxManager factory; the only
//     kernel-internal helper that wraps a known noop fallback for cells —
//     keeps cells/* free of any wrap call site)
func isWrapperCallerAllowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	if strings.HasPrefix(rel, "cmd/") {
		return true
	}
	switch rel {
	case "kernel/persistence/cell_marker.go",
		"kernel/outbox/cell_marker.go",
		"kernel/cell/demo_tx_runner.go":
		return true
	}
	parts := strings.Split(rel, "/")
	if len(parts) == 3 && parts[0] == "examples" && (parts[2] == "main.go" || parts[2] == "app.go") {
		return true
	}
	return false
}

// canonicalCalledFunc returns "<pkg-path>.<func-name>" for a CallExpr that
// resolves to a package-qualified function. Two AST forms map to the same
// callee:
//
//  1. *ast.SelectorExpr — `pkg.Func(...)` (normal import)
//  2. *ast.Ident         — `Func(...)` after `import . "pkg"` (dot-import)
//
// Without the *ast.Ident branch, dot-import would silently bypass the
// wrapper-location guard. info.Uses resolves either form to the same
// *types.Func with Pkg() pointing to the original package, so canonical
// matching is consistent across both spellings.
func canonicalCalledFunc(info *types.Info, call *ast.CallExpr) string {
	var ident *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		ident = fn.Sel
	case *ast.Ident:
		ident = fn
	default:
		return ""
	}
	obj := info.Uses[ident]
	if obj == nil {
		return ""
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return ""
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return ""
	}
	return pkg.Path() + "." + fn.Name()
}

func scanWrapperViolations(root string, resolver *typeseval.Resolver) []wrapperViolation {
	var out []wrapperViolation
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			rel, err := filepath.Rel(root, absPath)
			if err != nil {
				continue
			}
			relSlash := filepath.ToSlash(rel)
			scanner.EachNode[ast.CallExpr](file, func(call *ast.CallExpr) {
				canon := canonicalCalledFunc(pkg.TypesInfo, call)
				if !wrapperFunctionsCanonical[canon] {
					return
				}
				if isWrapperCallerAllowed(relSlash) {
					return
				}
				out = append(out, wrapperViolation{
					File:     relSlash,
					Line:     pkg.Fset.Position(call.Pos()).Line,
					FuncName: canon,
				})
			})
		}
	}
	return out
}

// allowlistDescription renders the allowlist as a single human-readable
// line for error messages. Single-source: every wrapper-location error
// references this same string instead of hand-copying the rule into each
// message — drift between code and message becomes structurally
// impossible.
func allowlistDescription() string {
	return "cmd/* | examples/<demo>/main.go | examples/<demo>/app.go | *_test.go | " +
		"kernel/{persistence,outbox}/cell_marker.go | kernel/cell/demo_tx_runner.go"
}

// wrapperFunctionsList renders the allowed wrapper function set as a
// sorted comma-joined list, derived from wrapperFunctionsCanonical so the
// rule definition and the error-message description never drift.
func wrapperFunctionsList() string {
	names := make([]string, 0, len(wrapperFunctionsCanonical))
	for fn := range wrapperFunctionsCanonical {
		names = append(names, fn)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// INVARIANT: CELL-RAW-INFRA-WRAPPER-LOCATION-01
//
// TestCellRawInfraWrapperLocation01_RealRepoClean verifies that no
// production code outside the authorized composition-root paths calls one
// of the wrapper functions. Wrapper-detection capability is verified by
// the sibling ScannerDetectsViolation test.
func TestCellRawInfraWrapperLocation01_RealRepoClean(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, false, nil, "./...")
	require.NoError(t, err)

	violations := scanWrapperViolations(root, resolver)
	for _, v := range violations {
		t.Errorf("CELL-RAW-INFRA-WRAPPER-LOCATION-01: %s:%d calls %s — caller not in composition-root allowlist (%s). Allowed wrappers: %s.",
			v.File, v.Line, v.FuncName, allowlistDescription(), wrapperFunctionsList())
	}
}

// INVARIANT: CELL-RAW-INFRA-WRAPPER-LOCATION-01
//
// TestCellRawInfraWrapperLocation01_ScannerDetectsViolation loads the
// build-tag-gated wrapfixture/violation package and asserts the scanner
// reports the wrap call as a violation (caller path is not allowlisted).
//
// Per ai-collab.md §"real source AST capture (AI 难造假)": the fixture is
// a real Go package loaded via packages.Load with the archtest_fixture
// build tag. Bypassing this test requires modifying real source code — a
// hand-crafted AST cannot satisfy go/types canonical-name resolution.
func TestCellRawInfraWrapperLocation01_ScannerDetectsViolation(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, false, []string{"archtest_fixture"},
		"./tools/archtest/internal/wrapfixture/violation")
	require.NoError(t, err)

	violations := scanWrapperViolations(root, resolver)
	require.NotEmpty(t, violations, "scanner must detect wrap calls from non-allowlisted fixture path")

	got := map[string]string{}
	gotLines := map[string][]int{}
	for _, v := range violations {
		got[v.FuncName] = v.File
		gotLines[v.FuncName] = append(gotLines[v.FuncName], v.Line)
		assert.True(t, strings.Contains(v.File, "wrapfixture"),
			"violation expected in wrapfixture path, got %s", v.File)
	}

	// The fixture covers all three wrapper functions; the scanner must
	// report each one. A pre-fix gap (only WrapForCell case in fixture)
	// silently masked detection regressions on the publisher/writer legs.
	assert.NotEmpty(t, got["github.com/ghbvf/gocell/kernel/persistence.WrapForCell"],
		"fixture must trigger persistence.WrapForCell detection")
	assert.NotEmpty(t, got["github.com/ghbvf/gocell/kernel/outbox.WrapPublisherForCell"],
		"fixture must trigger outbox.WrapPublisherForCell detection")
	assert.NotEmpty(t, got["github.com/ghbvf/gocell/kernel/outbox.WrapWriterForCell"],
		"fixture must trigger outbox.WrapWriterForCell detection")

	// dotimport.go uses `import . "kernel/outbox"` and writes the wrap
	// calls without a package selector — call.Fun is *ast.Ident, not
	// *ast.SelectorExpr. The scanner must walk the *ast.Ident branch via
	// info.Uses so dot-import bypasses cannot mask wrap-location
	// violations. We expect *both* the SelectorExpr-form (from violation.go)
	// and the dot-import form (from dotimport.go) to fire — i.e., the
	// publisher/writer wrappers each yield ≥ 2 line entries across the
	// two fixture files.
	assert.GreaterOrEqual(t, len(gotLines["github.com/ghbvf/gocell/kernel/outbox.WrapPublisherForCell"]), 2,
		"WrapPublisherForCell must be detected via SelectorExpr (violation.go) AND *ast.Ident dot-import (dotimport.go)")
	assert.GreaterOrEqual(t, len(gotLines["github.com/ghbvf/gocell/kernel/outbox.WrapWriterForCell"]), 2,
		"WrapWriterForCell must be detected via SelectorExpr (violation.go) AND *ast.Ident dot-import (dotimport.go)")
}
