// INVARIANT: REFRESH-COOKIE-SECURE-ATTRS-01

package archtest

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
)

// REFRESH-COOKIE-SECURE-ATTRS-01
//
// Claim: every http.Cookie composite literal in
// cells/accesscore/internal/httpcookie (production code) MUST set the three
// security attributes HttpOnly:true, Secure:true, SameSite:http.SameSiteStrictMode.
//
// # Why
//
// The whole point of BR-005 (#1278) is to store the refresh token in a cookie
// that survives a cold start (cross-refresh) yet is unreadable by XSS. That
// safety rests entirely on HttpOnly (XSS unreadable), Secure (HTTPS-only) and
// SameSite=Strict (CSRF). A future refactor that silently dropped HttpOnly would
// turn a long-lived refresh token into an XSS-readable credential — strictly
// worse than the memory-only status quo it replaces. A unit test asserting the
// wire string is Soft (a refactor can edit it away alongside the code); a P0
// security invariant earns a structural guard.
//
// # AI-robust grading
//
//   - Rating: Medium (AST form-lock on a single-package scope; the cookie is
//     built by the sole sanctioned constructor newRefreshCookie, and the three
//     attribute literals are pinned by AST shape — `true` Ident / SameSiteStrictMode
//     selector).
//   - Hard ceiling (honest): the holder is stdlib net/http.Cookie, whose Secure
//     / HttpOnly are public bool fields. The type system cannot make Secure:false
//     unexpressible, so a type-system Hard is unreachable here (same family as
//     the #851/#893 holder-seal ceilings). The directive provenance side IS Hard:
//     directiveCtxKey is unexported, so no cross-package code can forge a
//     SetRefresh directive.
//
// # Tool: archtest.Run(AST(DirsScope(...))) + EachInSubtree[ast.CompositeLit]
//
// # Blind spots (declared per ai-robust §强制盲区自检) + closures
//
//   - net/http imported under an alias / dot-import would make `http.Cookie` /
//     `http.SameSiteStrictMode` references non-canonical, dodging the AST name
//     match. CLOSED: scanRefreshCookieSecureAttrs flags any non-canonical
//     net/http import (imp.Name != nil) in the scanned package.
//   - The scope could become vacuous if newRefreshCookie were deleted/renamed so
//     no http.Cookie literal remains. CLOSED: anti-vacuity diagnostic when zero
//     http.Cookie literals are found.
//   - A weakened attribute value (e.g. Secure:false) passing the AST shape.
//     CLOSED: the `true` Ident / SameSiteStrictMode selector are matched by
//     exact shape, and the RED fixture (refreshcookiefixture, Secure:false) is
//     asserted to produce exactly one diagnostic in
//     TestRefreshCookieSecureAttrs_RedFixtureDetected.
//   - ACCEPTED (out of scope): cookies constructed OUTSIDE the httpcookie
//     package. The package is the single sanctioned cookie constructor for the
//     refresh token; cross-package raw http.SetCookie of the refresh token is a
//     review concern, not covered here (would be a different, broader rule).

func isHTTPCookieType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "http" && sel.Sel.Name == "Cookie"
}

func isTrueIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "true"
}

func isSameSiteStrictMode(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "http" && sel.Sel.Name == "SameSiteStrictMode"
}

func scanRefreshCookieSecureAttrs(p *Pass) []Diagnostic {
	var ds []Diagnostic
	cookieLits := 0
	for _, file := range p.Files {
		// Blind-spot closure: net/http must be a canonical (un-aliased) import so
		// the `http.` qualifier matched below is guaranteed to be net/http.
		for _, imp := range file.Imports {
			if imp.Path == nil || imp.Path.Value != `"net/http"` {
				continue
			}
			if imp.Name != nil {
				ds = append(ds, Diagnostic{
					Rel:     p.Rel(file),
					Line:    p.Fset.Position(imp.Pos()).Line,
					Message: "net/http must be imported without alias so http.Cookie / http.SameSiteStrictMode references stay canonical",
				})
			}
		}

		EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
			if !isHTTPCookieType(cl.Type) {
				return
			}
			cookieLits++
			attrs := map[string]ast.Expr{}
			EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					return
				}
				attrs[key.Name] = kv.Value
			})
			rel := p.Rel(file)
			line := p.Fset.Position(cl.Pos()).Line
			if !isTrueIdent(attrs["HttpOnly"]) {
				ds = append(ds, Diagnostic{Rel: rel, Line: line, Message: "http.Cookie must set HttpOnly: true"})
			}
			if !isTrueIdent(attrs["Secure"]) {
				ds = append(ds, Diagnostic{Rel: rel, Line: line, Message: "http.Cookie must set Secure: true"})
			}
			if !isSameSiteStrictMode(attrs["SameSite"]) {
				ds = append(ds, Diagnostic{Rel: rel, Line: line, Message: "http.Cookie must set SameSite: http.SameSiteStrictMode"})
			}
		})
	}
	if cookieLits == 0 {
		ds = append(ds, Diagnostic{
			Rel:     "(scope)",
			Line:    0,
			Message: "anti-vacuity: no http.Cookie composite literal found — refresh cookie constructor removed/renamed?",
		})
	}
	return ds
}

func TestRefreshCookieSecureAttrs(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	// Production scope only (default excludes _test.go): the package test file
	// builds bare inbound http.Cookie{} values that intentionally omit security
	// attributes; those are not production Set-Cookie construction.
	scope := DirsScope(root, []string{"cells/accesscore/internal/httpcookie"})

	diags := Run(t, AST(scope), scanRefreshCookieSecureAttrs)

	Report(t, "REFRESH-COOKIE-SECURE-ATTRS-01", diags)
}

// TestRefreshCookieSecureAttrs_RedFixtureDetected asserts the production scan
// catches the weakened-attribute fixture, so a zero-diagnostic outcome on the
// real package is informative (rule works) rather than vacuous (rule broken).
func TestRefreshCookieSecureAttrs_RedFixtureDetected(t *testing.T) {
	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/refreshcookiefixture/..."},
	), scanRefreshCookieSecureAttrs)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	// Exactly one hit: the fixture weakens Secure only (HttpOnly + SameSite kept
	// correct). Equality (not ≥1) pins the fixture so a drift in either the
	// fixture or the rule surfaces here.
	assert.Len(t, diags, 1,
		"refreshcookiefixture must yield exactly 1 REFRESH-COOKIE-SECURE-ATTRS-01 hit (Secure:false)")
}
