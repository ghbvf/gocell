// Package archtest enforces HANDLER-POLICY-REQUIRED-01 — the lone caller-side
// invariant from the original handler_invariants_test.go cluster that cannot
// be golden-pinned because it scans hand-written cells/.../cell.go wiring
// rather than codegen template output.
//
// PR-FUNNEL-02 (PR #411) migrated 4 of the 5 handler invariants to funnel-only
// enforcement (handler.tmpl + golden byte-pin in tools/codegen/contractgen):
//   - HANDLER-NO-INLINE-LIMIT-PARSE-01     -> pinned by http_order_list_v1 golden
//   - HANDLER-NO-SCHEMA-FOR-NOBODY-01      -> pinned by HasBody gate + http_order_get_v1 golden
//   - HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 -> pinned by synth_http_full / synth_http_keyword_conflict goldens
//   - HANDLER-VALIDATOR-FAIL-FAST-01       -> pinned by panic literal in all 8 handler goldens
//
// HANDLER-POLICY-REQUIRED-01 is the residual: it scans cell.go wiring for
// `<pkg>.NewHandler(svc, nil)` calls — a caller-side mistake that codegen
// goldens cannot reach. The funnel-first principle (CLAUDE.md) accepts
// archtest 平铺兜底 for constraints that cannot be funneled or type-system'd
// out; this file is that residual.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

const handlerPolicyRequiredRule = "HANDLER-POLICY-REQUIRED-01"

// handlerPolicyPublicExemptPkgs lists the import alias prefixes whose generated
// NewHandler is single-arg (Public=true endpoint) or whose route protection is
// provided by contract.Clients (RequireCallerCell guard).
//
//   - "registercontract" — http.device.register.v1 — Public:true, NewHandler(svc Service) single-arg
//   - "internallistcontract" — http.internal.devicecommands.list.v1 — /internal/v1/, Clients guard
var handlerPolicyPublicExemptPkgs = []string{
	"registercontract",
	"internallistcontract",
}

// INVARIANT: HANDLER-POLICY-REQUIRED-01
//
// Every cells/.../cell.go and examples/.../cell.go that wires a generated
// `<pkg>.NewHandler(svc, nil)` call must have <pkg> in
// handlerPolicyPublicExemptPkgs (Public:true contracts whose generated
// NewHandler takes a single argument, or internal endpoints where caller-cell
// gating substitutes for per-route policy). Otherwise the route mounts with
// `auth.Route{Policy: nil}` and any authenticated JWT can hit the handler —
// a silent fail-open.
//
// This rule cannot be replaced by handler.tmpl + golden byte-pin: golden
// freezes generator output (the NewHandler signature shape), not call sites.
// nil-policy is a CALLER mistake in hand-written cell.go; the funnel can't
// see it.
func TestHANDLER_POLICY_REQUIRED_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	cellFiles := collectCellGoFiles(t, root)

	var violations []string
	for _, f := range cellFiles {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		violations = append(violations, scanForNilPolicyNewHandler(f, rel)...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s: %s", handlerPolicyRequiredRule, v)
	}

	if len(violations) > 0 {
		t.Logf(`%s: %d violation(s) found. Fix: pass a non-nil auth.Policy to NewHandler.
For Public endpoints declare auth.public: true in contract.yaml and regenerate
— the generated NewHandler then accepts no policy argument, so the nil call
site disappears entirely.`, handlerPolicyRequiredRule, len(violations))
	}
}

// TestHANDLER_POLICY_REQUIRED_01_NegativeFixture verifies the scanner detects
// the violation pattern in the negative fixture file. Without this self-test
// a future refactor could break scanForNilPolicyNewHandler and the parent
// test would silently report 0 violations on a healthy tree.
func TestHANDLER_POLICY_REQUIRED_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	fixture := filepath.Join(root, "tools", "archtest", "testdata",
		"handler_nil_policy", "cell.go")
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Fatalf("negative fixture missing: %s", fixture)
	}

	rel := "tools/archtest/testdata/handler_nil_policy/cell.go"
	hits := scanForNilPolicyNewHandler(fixture, rel)
	if len(hits) == 0 {
		t.Errorf("negative fixture produced no violations — scanner broken")
	}
}

// collectCellGoFiles returns all cell.go files in cells/ and examples/ subtrees.
func collectCellGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string

	walk := func(dir string) {
		walkErr := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return fmt.Errorf("walk %s: %w", path, err)
			}
			if info == nil || info.IsDir() {
				return nil
			}
			if info.Name() == "cell.go" {
				files = append(files, path)
			}
			return nil
		})
		require.NoError(t, walkErr, "collectCellGoFiles: walking %s", dir)
	}
	walk("cells")
	walk("examples")
	return files
}

// scanForNilPolicyNewHandler parses the Go file at path and returns a list of
// violation strings for any call of the form <pkg>.NewHandler(<expr>, nil)
// where <pkg> is not in handlerPolicyPublicExemptPkgs.
func scanForNilPolicyNewHandler(path, rel string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse error: %v", rel, err)}
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "NewHandler" {
			return true
		}
		if len(call.Args) != 2 {
			return true
		}
		ident, ok := call.Args[1].(*ast.Ident)
		if !ok || ident.Name != "nil" {
			return true
		}
		pkgAlias := ""
		if id, ok := sel.X.(*ast.Ident); ok {
			pkgAlias = id.Name
		}
		for _, exempt := range handlerPolicyPublicExemptPkgs {
			if pkgAlias == exempt {
				return true
			}
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: %s.NewHandler called with nil policy — non-public endpoint must supply a real auth.Policy",
			rel, pos.Line, pkgAlias,
		))
		return true
	})
	return violations
}
