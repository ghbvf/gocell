// INVARIANT: HANDLER-POLICY-REQUIRED-01
//
// Package archtest enforces HANDLER-POLICY-REQUIRED-01 — the simplified caller-side
// invariant from the original handler_invariants_test.go cluster that cannot
// be golden-pinned because it scans hand-written cells/... and examples/... wiring
// rather than codegen template output.
//
// PR-FUNNEL-02 (PR #411) migrated 4 of the 5 handler invariants to funnel-only
// enforcement (handler.tmpl + golden byte-pin in tools/codegen/contractgen):
//   - HANDLER-NO-INLINE-LIMIT-PARSE-01     -> pinned by http_order_list_v1 golden
//   - HANDLER-NO-SCHEMA-FOR-NOBODY-01      -> pinned by HasBody gate + http_order_get_v1 golden
//   - HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 -> pinned by synth_http_full / synth_http_keyword_conflict goldens
//   - HANDLER-VALIDATOR-FAIL-FAST-01       -> pinned by panic literal in all handler goldens
//
// HANDLER-POLICY-REQUIRED-01 funnel-first upgrade (F1 fix):
//
// The funnel end is the primary defense:
//   - Public/ClientsOnly/ServiceOwned endpoints generate single-arg NewHandler(svc Service) — there
//     is no second argument to pass nil to; the call site cannot violate the invariant.
//   - Default branch (non-public, non-bootstrap, non-clientsOnly, non-serviceOwned) NewHandler now has
//     a construction-time `if policy == nil { panic(errcode.Assertion(...)) }` — a
//     2-arg call with a typed nil panics at startup rather than silently fail-open.
//
// This archtest is the simplified flat backstop: it catches "caller passes the
// literal nil to a 2-arg NewHandler but hasn't updated contract.yaml to declare
// public/clientsOnly/serviceOwned yet" — code that would panic at startup anyway, but which
// the scanner catches earlier (in CI) rather than at service boot.
//
// No exemption list needed: the funnel guarantees that any legitimate single-arg
// call site uses a contract-declared Public, ClientsOnly, or ServiceOwned flag, which changes the
// generated signature to 1-arg. Any 2-arg site passing literal nil is always wrong.
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

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const handlerPolicyRequiredRule = "HANDLER-POLICY-REQUIRED-01"

// INVARIANT: HANDLER-POLICY-REQUIRED-01
//
// Funnel end (primary defense): handler.tmpl generates single-arg NewHandler for
// Public/ClientsOnly/ServiceOwned endpoints and a construction-time nil-panic for the default
// 2-arg branch. This archtest is the simplified flat backstop that catches the
// code-smell earlier (in CI) rather than at service startup.
//
// Scan: any `<pkg>.NewHandler(<expr>, nil)` call in production .go files under cells/ or examples/
// is a violation — no exemption list. Rationale:
//   - Legitimate single-arg call sites use a contract-declared Public, ClientsOnly, or ServiceOwned
//     flag → codegen changes the signature to 1-arg → the 2-arg site disappears.
//   - The old handlerPolicyPublicExemptPkgs alias-string exemption was fragile
//     (alias spoofing, import renaming). The new funnel makes it unnecessary.
//   - Typed-nil paths (var pol auth.Policy; NewHandler(svc, pol)) are NOT caught
//     here — they are caught at service startup by the construction-time panic.
//     The scanner would need type information (go/types) to detect them statically;
//     the runtime panic is a sufficient and simpler backstop for that path.
//
// This rule cannot be replaced by handler.tmpl + golden byte-pin: golden freezes
// generator output (the NewHandler signature shape), not call sites. A literal nil
// at the call site in hand-written cell.go is a CALLER mistake the funnel cannot see.
func TestHANDLER_POLICY_REQUIRED_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	cellFiles := collectProductionGoFiles(t, root)

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
		t.Logf(`%s: %d violation(s) found.
Fix: all 2-arg NewHandler call sites must supply a non-nil auth.Policy.
For public/clients-only/service-owned endpoints regenerate after declaring auth.public/auth.clientsOnly/auth.serviceOwned
in contract.yaml — the generated NewHandler then takes no policy argument,
so the nil call site disappears entirely.`, handlerPolicyRequiredRule, len(violations))
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
		"handler_nil_policy", "slice_handler.go")
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Fatalf("negative fixture missing: %s", fixture)
	}

	rel := "tools/archtest/testdata/handler_nil_policy/slice_handler.go"
	hits := scanForNilPolicyNewHandler(fixture, rel)
	if len(hits) == 0 {
		t.Errorf("negative fixture produced no violations — scanner broken")
	}
}

// collectProductionGoFiles returns all production Go files in cells/ and examples/ subtrees.
func collectProductionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	scope := scanner.DirsScope(root, []string{"cells", "examples"})
	files, err := scope.Files()
	require.NoError(t, err, "collectProductionGoFiles: DirsScope.Files")
	sort.Strings(files)
	return files
}

// scanForNilPolicyNewHandler parses the Go file at path and returns a list of
// violation strings for any call of the form <pkg>.NewHandler(<expr>, nil).
// No exemption list: the funnel guarantees that all legitimate single-arg call
// sites use a contract-declared Public, ClientsOnly, or ServiceOwned flag (which produces a
// 1-arg generated signature), so any 2-arg site with literal nil is always wrong.
func scanForNilPolicyNewHandler(path, rel string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse error: %v", rel, err)}
	}

	var violations []string
	scanner.EachNode[ast.CallExpr](f, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name != "NewHandler" {
			return
		}
		if len(call.Args) != 2 {
			return
		}
		ident, ok := call.Args[1].(*ast.Ident)
		if !ok || ident.Name != "nil" {
			return
		}
		pkgAlias := ""
		if id, ok := sel.X.(*ast.Ident); ok {
			pkgAlias = id.Name
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: %s.NewHandler called with literal nil policy — "+
				"all 2-arg NewHandler call sites must supply a non-nil auth.Policy; "+
				"for public/clients-only/service-owned endpoints regenerate after declaring "+
				"auth.public/auth.clientsOnly/auth.serviceOwned in contract.yaml",
			rel, pos.Line, pkgAlias,
		))
	})
	return violations
}
