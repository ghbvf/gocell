//go:build archtest_fixture

// Package refreshcookiefixture is a RED fixture for
// REFRESH-COOKIE-SECURE-ATTRS-01. It constructs an *http.Cookie with a weakened
// security attribute (Secure:false) so the production scan
// (scanRefreshCookieSecureAttrs) MUST report exactly one diagnostic. Without
// this fixture the rule's zero-diagnostic outcome on the real httpcookie
// package would carry no information (rule works AND no violation, vs rule
// silently broken). Gated by the archtest_fixture build tag; never compiled in
// normal builds.
package refreshcookiefixture

import "net/http"

// weakCookie intentionally weakens Secure. HttpOnly and SameSite are kept
// correct so the fixture exercises exactly one missing-attribute branch (the
// expected-count assertion in the RED-fixture test pins this to 1).
func weakCookie() *http.Cookie {
	return &http.Cookie{
		Name:     "gocell_rt",
		Value:    "x",
		Path:     "/api/v1/access/sessions",
		MaxAge:   604800,
		HttpOnly: true,
		Secure:   false, // RED: weakened — must be caught
		SameSite: http.SameSiteStrictMode,
	}
}
