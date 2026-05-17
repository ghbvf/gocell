// invariants:
//   - INVARIANT: HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01
//   - INVARIANT: HEALTH-REDACTED-ERROR-MSG-FUNNEL-01
//   - INVARIANT: HEALTH-VERBOSE-SCAN-COVERAGE-01
//
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 — runtime/http/health.verboseDependencyEntry
//
//	struct field set is exactly {Status, DurationMs}. Adding error text to the
//	wire payload requires extending this allowlist deliberately + amending ADR
//	docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md
//	§3 (channel mapping) and §6 (enforcement funnel matrix).
//
// HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 — Production runtime/http/health/ code may
//
//	construct redactedErrorMsg values only via newRedactedErrorMsg. Any other
//	type conversion `redactedErrorMsg(x)` outside newRedactedErrorMsg's function
//	body fails this gate. Hard funnel: combined with the type being unexported,
//	no external package can construct a redactedErrorMsg by any shape.
//
// HEALTH-VERBOSE-SCAN-COVERAGE-01 — fail-closed coverage gate. Asserts the
//
//	walker used by the two Hard rules above enumerates every non-test .go file
//	under runtime/http/health/ (recursive). A future sub-package or filename
//	quirk that drops files out of the scan would silently bypass the gates;
//	this test catches the slip.
//
// Blind-spot inventory (charter §3 mandatory) for HEALTH-REDACTED-ERROR-MSG-FUNNEL-01:
//
//	(a) untyped string literal conversion via assignment to ErrorMsg field
//	    (e.g. `slogDependencyEntry{ErrorMsg: "raw"}`) — empty literal "" is
//	    the documented nil sentinel, any non-empty literal indicates bypass.
//	    Reverse-checked by TestHealthRedactedErrorMsgFunnelLiteralReverse.
//	(b) reflect-based construction — requires reflect.Value.Convert on the
//	    redactedErrorMsg type; the type being unexported makes this impossible
//	    to reach from outside the package, and using reflect within the
//	    package would itself be the bug being investigated. No additional
//	    archtest needed — the package boundary closes the upstream side.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleHealthVerboseWireShapeFrozen     = "HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01"
	ruleHealthRedactedErrorMsgFunnel     = "HEALTH-REDACTED-ERROR-MSG-FUNNEL-01"
	ruleHealthVerboseScanCoverage        = "HEALTH-VERBOSE-SCAN-COVERAGE-01"
	healthPackageRelativeRoot            = "runtime/http/health"
	healthVerboseShapeName               = "verboseDependencyEntry"
	healthRedactedErrorMsgTypeName       = "redactedErrorMsg"
	healthRedactedErrorMsgFunnelFuncName = "newRedactedErrorMsg"
	healthSlogEntryTypeName              = "slogDependencyEntry"
)

// healthVerboseWireAllowedFields is the verbatim field set of
// runtime/http/health.verboseDependencyEntry. Adding a field requires
// extending this allowlist deliberately and amending ADR
// 202605171200-adr-readyz-verbose-four-channel-redaction.md §3 to declare
// which channel the new field belongs to.
var healthVerboseWireAllowedFields = map[string]struct{}{
	"Status":     {},
	"DurationMs": {},
}

// TestHealthVerboseWireShapeFrozen enforces HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01.
func TestHealthVerboseWireShapeFrozen(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	pkgDir := filepath.Join(root, healthPackageRelativeRoot)
	fset := token.NewFileSet()

	var (
		found   bool
		seen    = make(map[string]struct{})
		unknown []string
	)
	require.NoError(t, eachHealthProductionFile(pkgDir, func(path string) error {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
			if ts.Name == nil || ts.Name.Name != healthVerboseShapeName {
				return
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return
			}
			found = true
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					line := fset.Position(field.Type.Pos()).Line
					unknown = append(unknown, fmt.Sprintf("%s:%d: <embedded field>", rel, line))
					continue
				}
				for _, name := range field.Names {
					seen[name.Name] = struct{}{}
					if _, ok := healthVerboseWireAllowedFields[name.Name]; !ok {
						line := fset.Position(name.Pos()).Line
						unknown = append(unknown, fmt.Sprintf("%s:%d: %s", rel, line, name.Name))
					}
				}
			}
		})
		return nil
	}))

	if !found {
		t.Fatalf("%s: %s struct definition not found under %s — if the type was relocated, "+
			"update this test's hardcoded type name + relative root along with the move",
			ruleHealthVerboseWireShapeFrozen, healthVerboseShapeName, healthPackageRelativeRoot)
	}

	var missing []string
	for k := range healthVerboseWireAllowedFields {
		if _, ok := seen[k]; !ok {
			missing = append(missing, k)
		}
	}

	sort.Strings(unknown)
	sort.Strings(missing)
	for _, u := range unknown {
		t.Errorf("%s: %s — field not in allowlist; the wire shape carries no error text by "+
			"design (channel d ops-diagnostics owns it). Adding a field requires updating "+
			"healthVerboseWireAllowedFields and amending ADR "+
			"docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md §3+§6",
			ruleHealthVerboseWireShapeFrozen, u)
	}
	for _, m := range missing {
		t.Errorf("%s: required field %s missing from %s.%s — removing a field changes the wire payload; review ADR 202605171200",
			ruleHealthVerboseWireShapeFrozen, m, healthPackageRelativeRoot, healthSlogEntryTypeName)
	}
}

// TestHealthRedactedErrorMsgFunnel enforces HEALTH-REDACTED-ERROR-MSG-FUNNEL-01.
//
// Detection (pure AST, no go/types — scope is one directory, the type being
// unexported closes the package boundary):
//  1. Walk every non-test .go file under runtime/http/health/ recursively.
//  2. For each file, traverse FuncDecls. Inside each function body, find every
//     *ast.CallExpr whose Fun is *ast.Ident{Name: "redactedErrorMsg"}.
//  3. Assert the enclosing FuncDecl.Name is "newRedactedErrorMsg".
func TestHealthRedactedErrorMsgFunnel(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	pkgDir := filepath.Join(root, healthPackageRelativeRoot)
	fset := token.NewFileSet()

	var violations []string
	require.NoError(t, eachHealthProductionFile(pkgDir, func(path string) error {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fnName := ""
			if fd.Name != nil {
				fnName = fd.Name.Name
			}
			EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || ident.Name != healthRedactedErrorMsgTypeName {
					return
				}
				if fnName != healthRedactedErrorMsgFunnelFuncName {
					line := fset.Position(call.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: redactedErrorMsg(...) conversion inside %s; only %s may construct redactedErrorMsg values",
						rel, line, fnName, healthRedactedErrorMsgFunnelFuncName,
					))
				}
			})
		}
		return nil
	}))

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s: %s", ruleHealthRedactedErrorMsgFunnel, v)
	}
}

// TestHealthRedactedErrorMsgFunnelLiteralReverse is the blind-spot reverse
// self-check for HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 case (a): asserts that
// no slogDependencyEntry composite literal in runtime/http/health/ sets
// ErrorMsg to a non-empty string literal (which would bypass the funnel via
// untyped const conversion). Empty literal "" is allowed as the documented
// nil sentinel.
func TestHealthRedactedErrorMsgFunnelLiteralReverse(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	pkgDir := filepath.Join(root, healthPackageRelativeRoot)
	fset := token.NewFileSet()

	var violations []string
	require.NoError(t, eachHealthProductionFile(pkgDir, func(path string) error {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		EachInSubtree[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
			ident, ok := cl.Type.(*ast.Ident)
			if !ok || ident.Name != healthSlogEntryTypeName {
				return
			}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				keyIdent, ok := kv.Key.(*ast.Ident)
				if !ok || keyIdent.Name != "ErrorMsg" {
					continue
				}
				lit, ok := kv.Value.(*ast.BasicLit)
				if !ok {
					// Not a literal — typed call result (newRedactedErrorMsg etc).
					// The forward TestHealthRedactedErrorMsgFunnel covers the
					// conversion-side rule; here we only watch literal bypass.
					continue
				}
				if lit.Kind == token.STRING && lit.Value != `""` {
					line := fset.Position(lit.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: slogDependencyEntry.ErrorMsg literal %s — only \"\" sentinel or newRedactedErrorMsg(...) result is allowed",
						rel, line, lit.Value,
					))
				}
			}
		})
		return nil
	}))

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s (literal reverse self-check): %s", ruleHealthRedactedErrorMsgFunnel, v)
	}
}

// TestHealthVerboseScanCoverage enforces HEALTH-VERBOSE-SCAN-COVERAGE-01.
//
// Compares the file set yielded by eachHealthProductionFile (the walker the
// two Hard rules above consume) against a ground-truth walk over the same
// directory tree using only filename predicate "non-test .go". A mismatch
// indicates the walker silently drops files due to a sub-package quirk,
// build tag, or filename oddity — which would let a future bypass slip past
// the Hard rules without warning.
func TestHealthVerboseScanCoverage(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	pkgDir := filepath.Join(root, healthPackageRelativeRoot)

	var scanned []string
	require.NoError(t, eachHealthProductionFile(pkgDir, func(path string) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		scanned = append(scanned, rel)
		return nil
	}))

	var truth []string
	require.NoError(t, filepath.WalkDir(pkgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		truth = append(truth, rel)
		return nil
	}))

	sort.Strings(scanned)
	sort.Strings(truth)
	assert.Equal(t, truth, scanned,
		"%s: eachHealthProductionFile must enumerate every non-test .go file under %s; "+
			"missing files would silently bypass HEALTH-VERBOSE-* gates",
		ruleHealthVerboseScanCoverage, healthPackageRelativeRoot)

	// Defense in depth: ground-truth set must include the canonical files
	// where the Hard rules expect to find their targets. A future file move
	// that relocates these out of the directory would fail this assertion
	// before reaching the Hard rules themselves.
	requireContains(t, truth, filepath.Join(healthPackageRelativeRoot, "verbose_shape.go"))
	requireContains(t, truth, filepath.Join(healthPackageRelativeRoot, "health.go"))
}

// eachHealthProductionFile walks runtime/http/health/ recursively and invokes
// visit on every non-test .go file. Single source of truth for the file set
// scanned by HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 and
// HEALTH-REDACTED-ERROR-MSG-FUNNEL-01.
func eachHealthProductionFile(pkgDir string, visit func(path string) error) error {
	return filepath.WalkDir(pkgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		return visit(path)
	})
}

func requireContains(t *testing.T, items []string, want string) {
	t.Helper()
	for _, item := range items {
		if item == want {
			return
		}
	}
	t.Fatalf("expected %q in %v", want, items)
}
