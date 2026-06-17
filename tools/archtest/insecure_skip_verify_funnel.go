// Importable rule body for INSECURE-SKIP-VERIFY-LITERAL-01. Non-test .go file
// so the rule is module-path-agnostic — platform symbol paths are derived from
// [PlatformFrameworkModulePath] (external.go), NOT bare string literals
// (ARCHTEST-MODULE-PATH-FUNNEL-01). The dogfood + RED-fixture precision gate
// live in insecure_skip_verify_funnel_test.go.
//
// Not registered in StandardCellRules: the allowed site
// (framework/runtime/http/tlsutil) is a GoCell-internal package; an external
// Cell repo has no such file, so the allowlist is vacuous and the rule
// degrades to a pure repo-wide ban — which is the correct posture for an
// external consumer that should never set InsecureSkipVerify:true. An
// ExtraRule wire is the correct way to pick it up. Kept importable +
// module-path-agnostic but OUT of StandardCellRules.
//
// # INSECURE-SKIP-VERIFY-LITERAL-01
//
// No PRODUCTION .go file may set the composite-literal struct field
//
//	InsecureSkipVerify: true
//
// (a KeyValueExpr whose Key ident is "InsecureSkipVerify" and Value is the
// "true" ident) EXCEPT inside framework/runtime/http/tlsutil (the sole
// sanctioned home, where the field is paired with a VerifyConnection callback
// that performs the full SPIFFE-ID + chain check — see tlsutil.NewClientMTLSConfig
// godoc).
//
// This catches a future fail-open `&tls.Config{InsecureSkipVerify: true}`
// written without the compensating VerifyConnection verifier. It is DISTINCT
// from SEC-FAIL-CLOSED-04 (security_defaults.go), which scans only
// adapters/websocket for the ASSIGNMENT form
//
//	opts.InsecureSkipVerify = true
//
// This rule covers the composite-literal FIELD form repo-wide.
//
// # AI-robust: Medium
//
// The composite literal `InsecureSkipVerify: true` is expressible anywhere —
// this is exactly the Go-language ceiling that prevents Hard. The rule uses
// text-free AST scan (KeyValueExpr key ident + value ident) across all
// production .go files, so it fires on every future literal regardless of
// variable name, struct type name, or import alias. Anti-vacuity proven by the
// RED fixture in insecure_skip_verify_funnel_test.go.
//
// # Allowed site
//
// framework/runtime/http/tlsutil (package dir prefix, bound to platform
// package identity via isGoCellPlatformPkgPath). The single current literal is
// inside NewClientMTLSConfig where InsecureSkipVerify:true is paired with a
// VerifyConnection callback. Adding a second literal inside tlsutil requires
// the same pairing (enforced by code review; the archtest only closes the
// out-of-package bypass vector).
//
// # _test.go scope
//
// _test.go files are skipped by rel-suffix filter — tests may legitimately
// construct tls.Config{InsecureSkipVerify: true} for local dial without a
// peer verifier.
//
// # Blind spots (BS)
//
//   - BS-1 Variable assignment: `cfg.InsecureSkipVerify = true` (AssignStmt)
//     is covered by SEC-FAIL-CLOSED-04 (adapters/websocket scope). This rule
//     covers the composite-literal form only; assignment form outside
//     adapters/websocket is a gap shared with SEC-04's scoped design.
//   - BS-2 Indirect true: `var t = true; tls.Config{InsecureSkipVerify: t}`
//     — the Value is *ast.Ident with Name != "true", so it is not flagged.
//     Accepted: fabricating a true-alias to circumvent the check requires
//     deliberate evasion, outside the incidental-mistake threat model.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const insecureSkipVerifyFunnelRuleID = "INSECURE-SKIP-VERIFY-LITERAL-01"

// CheckInsecureSkipVerifyLiteral enforces INSECURE-SKIP-VERIFY-LITERAL-01:
// no production file outside framework/runtime/http/tlsutil may use the
// composite-literal field InsecureSkipVerify: true. It scans the running
// module's full production tree and returns the diagnostics it observes;
// GoCell's TestInsecureSkipVerifyLiteral calls it directly — single source,
// no parallel rule body.
func CheckInsecureSkipVerifyLiteral(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	files, err := findAllProductionGoFiles(root)
	if err != nil {
		t.Fatalf("%s: collecting production files: %v", insecureSkipVerifyFunnelRuleID, err)
	}
	var diags []Diagnostic
	for _, f := range files {
		hits, ferr := findInsecureSkipVerifyLiteral(f)
		if ferr != nil {
			t.Fatalf("%s: scanning %s: %v", insecureSkipVerifyFunnelRuleID, f, ferr)
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if isInsecureSkipVerifyAllowedRel(rel) {
			continue
		}
		for _, line := range hits {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"composite-literal InsecureSkipVerify: true outside the sanctioned site "+
						"(framework/runtime/http/tlsutil); "+
						"pair it with a VerifyConnection callback or remove it "+
						"(%s)", insecureSkipVerifyFunnelRuleID,
				),
			})
		}
	}
	return diags
}

// findInsecureSkipVerifyLiteral parses path and returns the line numbers of
// every composite-literal KeyValueExpr of the form
//
//	InsecureSkipVerify: true
//
// where Key is an *ast.Ident named "InsecureSkipVerify" and Value is an
// *ast.Ident named "true". This shape is unambiguous regardless of struct type
// name (tls.Config, or any hypothetical future type) or import alias.
func findInsecureSkipVerifyLiteral(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	// Fast-path: skip files that don't contain the literal bytes at all.
	if !strings.Contains(string(data), "InsecureSkipVerify") {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	EachInSubtree[ast.KeyValueExpr](f, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "InsecureSkipVerify" {
			return
		}
		val, ok := kv.Value.(*ast.Ident)
		if !ok || val.Name != "true" {
			return
		}
		lines = append(lines, fset.Position(kv.Pos()).Line)
	})
	return lines, nil
}

// isInsecureSkipVerifyAllowedRel reports whether the module-relative file path
// rel is inside the sole allowed package directory
// (framework/runtime/http/tlsutil). Files in this package are the only
// sanctioned home for InsecureSkipVerify: true (paired with VerifyConnection).
//
// rel is a slash-separated module-relative path (e.g.
// "framework/runtime/http/tlsutil/client.go"). The check is a HasPrefix on the
// directory ("framework/runtime/http/tlsutil/") so that only files directly in
// that package are allowed — not a hypothetical sibling like
// "framework/runtime/http/tlsutil2/".
//
// The exemption is intentionally a FILE-PATH check (not a go/types pkgPath
// check) because CheckInsecureSkipVerifyLiteral uses a text-mode AST parse
// (findInsecureSkipVerifyLiteral) rather than a Typed Run pass. The
// filesystem path is authoritative within a single module: every file under
// framework/runtime/http/tlsutil/ belongs to package tlsutil, so there is no
// bypass surface.
func isInsecureSkipVerifyAllowedRel(rel string) bool {
	const allowedPrefix = "framework/runtime/http/tlsutil/"
	return strings.HasPrefix(rel, allowedPrefix)
}
