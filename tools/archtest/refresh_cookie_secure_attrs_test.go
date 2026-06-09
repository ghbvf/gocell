//   - INVARIANT: REFRESH-COOKIE-SECURE-ATTRS-01
//   - INVARIANT: REFRESH-COOKIE-SINGLE-WRITER-01

package archtest

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// REFRESH-COOKIE-SECURE-ATTRS-01
//
// Claim (two parts):
//
//  1. Secure attributes — every http.Cookie composite literal in
//     corecells/accesscore/internal/httpcookie (production code) MUST set the three
//     security attributes HttpOnly:true, Secure:true,
//     SameSite:http.SameSiteStrictMode (scanRefreshCookieSecureAttrs).
//  2. Host-binding — the package's CookieName const MUST be "__Host-gocell_rt"
//     and CookiePath MUST be "/", and no http.Cookie literal may set a Domain
//     (scanRefreshCookieHostBound). The browser-enforced __Host- prefix binds
//     the long-lived, cookie-first refresh credential to the exact host, so a
//     sibling subdomain cannot set or override it (cookie tossing / fixation);
//     the prefix is only honored when Secure + Path=/ + no Domain all hold.
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
//   - Cross-package writes of the refresh cookie are NO LONGER out of scope:
//     REFRESH-COOKIE-SINGLE-WRITER-01 (scanRefreshCookieSingleWriter, below)
//     enforces that the refresh-cookie name may only be written inside the
//     httpcookie package. (Superseded the pre-#1677 "ACCEPTED out of scope"
//     carve-out.)
//
// REFRESH-COOKIE-SINGLE-WRITER-01
//
// Claim: the refresh-cookie name "__Host-gocell_rt" may be written ONLY inside
// corecells/accesscore/internal/httpcookie. Any other production package that emits
// a cookie with this name (via http.Cookie{Name:...}, raw Set-Cookie header, or
// the httpcookie.CookieName const) is a single-writer violation — it would let
// a second site mint or weaken the host-bound refresh credential outside the
// one audited constructor.
//
// # Tool: archtest.Run(Production(TypedOpts{})) + EachInSubtree
//
// Two checks per production file (httpcookie package skipped):
//
//   - Check A (AST, always): a bare string literal == "__Host-gocell_rt".
//     Covers http.Cookie{Name:"..."} AND a raw Set-Cookie header string write.
//   - Check B (typed, when the Pass carries types): an http.Cookie{Name: X}
//     where X is NOT a literal but EvaluateConstString resolves it to the
//     sentinel (e.g. a reference to httpcookie.CookieName). Closes the
//     const-reference blind spot that Check A's literal scan would miss.
//
// # AI-robust grading (REFRESH-COOKIE-SINGLE-WRITER-01)
//
//   - Rating: Medium both axes. Downstream: the scan resolves the sentinel via
//     EvaluateConstString (literal / local const / cross-package const all
//     match) + a bare-literal AST scan for raw header writes; RED fixture proves
//     it is non-vacuous. Upstream Medium (honest ceiling): a cookie name is just
//     a string to net/http, so Go cannot make "only httpcookie emits this name"
//     unexpressible — same #851/#893/#1282 family (won't-do).
//   - Blind spots (declared per ai-robust §强制盲区自检) + closures:
//   - String-concat name ("__Host-" + "gocell_rt") dodges both checks. OPEN
//     (declared): implausible + review-visible; same class as other
//     string-anchor blind spots.
//   - Field-by-field assignment (c := &http.Cookie{}; c.Name = sentinel) —
//     Check A still catches the sentinel literal; only a const-ref assigned
//     field-by-field outside an http.Cookie literal escapes. OPEN (declared).
//   - net/http imported under an alias would make Check B's isHTTPCookieType
//     miss the composite literal; Check A (literal) is unaffected. OPEN for the
//     const-ref-via-aliased-http.Cookie corner only (declared).
//   - Vacuity: a mis-scoped scan would silently pass (0 hits). CLOSED:
//     TestRefreshCookieSingleWriter_RedFixtureDetected asserts the fixture
//     (a cross-package sentinel writer) yields exactly one diagnostic.

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
	scope := DirsScope(root, []string{"corecells/accesscore/internal/httpcookie"})

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

// refreshCookieName is the sentinel refresh-cookie name. Single source for the
// host-bound shape check and the cross-package single-writer scan.
const refreshCookieName = "__Host-gocell_rt"

// httpcookiePkgRel is the module-relative path of the sole sanctioned writer of
// the refresh cookie (REFRESH-COOKIE-SINGLE-WRITER-01 skips it).
const httpcookiePkgRel = "corecells/accesscore/internal/httpcookie"

// scanRefreshCookieHostBound enforces the host-binding half of
// REFRESH-COOKIE-SECURE-ATTRS-01 inside the httpcookie package: CookieName must
// be the __Host- sentinel, CookiePath must be "/", and no http.Cookie literal
// may set a Domain. Anti-vacuity fires if either const is missing.
func scanRefreshCookieHostBound(p *Pass) []Diagnostic {
	var ds []Diagnostic
	const wantName = `"` + refreshCookieName + `"`
	const wantPath = `"/"`
	foundName, foundPath := false, false
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInChildren[ast.GenDecl](file, func(gd *ast.GenDecl) {
			if gd.Tok != token.CONST {
				return
			}
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok {
						continue
					}
					line := p.Fset.Position(lit.Pos()).Line
					switch name.Name {
					case "CookieName":
						foundName = true
						if lit.Value != wantName {
							ds = append(ds, Diagnostic{
								Rel: rel, Line: line,
								Message: "CookieName must be " + wantName + " (the __Host- prefix is browser-enforced host-binding)",
							})
						}
					case "CookiePath":
						foundPath = true
						if lit.Value != wantPath {
							ds = append(ds, Diagnostic{
								Rel: rel, Line: line,
								Message: "CookiePath must be " + wantPath + " (mandated by the __Host- prefix)",
							})
						}
					}
				}
			})
		})
		EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
			if !isHTTPCookieType(cl.Type) {
				return
			}
			EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Domain" {
					ds = append(ds, Diagnostic{
						Rel: rel, Line: p.Fset.Position(kv.Pos()).Line,
						Message: "refresh cookie must not set Domain (the __Host- prefix forbids it)",
					})
				}
			})
		})
	}
	if !foundName {
		ds = append(ds, Diagnostic{
			Rel: "(scope)", Line: 0,
			Message: "anti-vacuity: CookieName const not found in httpcookie package",
		})
	}
	if !foundPath {
		ds = append(ds, Diagnostic{
			Rel: "(scope)", Line: 0,
			Message: "anti-vacuity: CookiePath const not found in httpcookie package",
		})
	}
	return ds
}

// scanRefreshCookieSingleWriter enforces REFRESH-COOKIE-SINGLE-WRITER-01: the
// refresh-cookie name may only be written inside the httpcookie package. See the
// file-head godoc for Check A / Check B and the blind-spot inventory.
func scanRefreshCookieSingleWriter(p *Pass) []Diagnostic {
	var ds []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if rel == httpcookiePkgRel || strings.HasPrefix(rel, httpcookiePkgRel+"/") {
			continue // the single sanctioned writer
		}
		// Check A: bare string literal == sentinel (covers http.Cookie{Name:"…"}
		// AND a raw Set-Cookie header string write).
		EachInSubtree[ast.BasicLit](file, func(lit *ast.BasicLit) {
			if lit.Kind != token.STRING {
				return
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && s == refreshCookieName {
				ds = append(ds, Diagnostic{
					Rel: rel, Line: p.Fset.Position(lit.Pos()).Line,
					Message: "refresh cookie name " + strconv.Quote(refreshCookieName) + " may only be written in " + httpcookiePkgRel,
				})
			}
		})
		// Check B: http.Cookie{Name: <non-literal const>} resolving to sentinel
		// (covers a const ref such as httpcookie.CookieName; needs types).
		if !p.Typed() {
			continue
		}
		EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
			if !isHTTPCookieType(cl.Type) {
				return
			}
			EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Name" {
					return
				}
				if _, isLit := kv.Value.(*ast.BasicLit); isLit {
					return // already covered by Check A
				}
				if s, ok := EvaluateConstString(p.TypesInfo, kv.Value); ok && s == refreshCookieName {
					ds = append(ds, Diagnostic{
						Rel: rel, Line: p.Fset.Position(kv.Pos()).Line,
						Message: "refresh cookie name (via const) may only be written in " + httpcookiePkgRel,
					})
				}
			})
		})
	}
	return ds
}

// TestRefreshCookieHostBound asserts the host-binding shape (CookieName / Path /
// no Domain) on the real httpcookie package.
func TestRefreshCookieHostBound(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"corecells/accesscore/internal/httpcookie"})

	diags := Run(t, AST(scope), scanRefreshCookieHostBound)

	Report(t, "REFRESH-COOKIE-SECURE-ATTRS-01", diags)
}

// TestRefreshCookieSingleWriter asserts no production package outside httpcookie
// writes the refresh-cookie name.
func TestRefreshCookieSingleWriter(t *testing.T) {
	t.Parallel()

	diags := Run(t, Production(TypedOpts{}), scanRefreshCookieSingleWriter)

	Report(t, "REFRESH-COOKIE-SINGLE-WRITER-01", diags)
}

// TestRefreshCookieSingleWriter_RedFixtureDetected asserts the cross-package
// scan catches a sentinel-named cookie written outside httpcookie, so a
// zero-diagnostic outcome on real production is informative (rule works) rather
// than vacuous (rule mis-scoped).
func TestRefreshCookieSingleWriter_RedFixtureDetected(t *testing.T) {
	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/refreshcookiefixture/..."},
	), scanRefreshCookieSingleWriter)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	// Exactly one hit: the fixture's crossWriterCookie sets Name to the sentinel
	// once. Equality pins the fixture so drift in either side surfaces here.
	assert.Len(t, diags, 1,
		"refreshcookiefixture must yield exactly 1 REFRESH-COOKIE-SINGLE-WRITER-01 hit (cross-package __Host-gocell_rt writer)")
}
