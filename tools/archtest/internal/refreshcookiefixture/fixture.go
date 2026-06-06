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

// fixtureMaxAge mirrors accesscore.DefaultRefreshMaxAge (7 days in seconds).
// Named to avoid bare magic literals and to stay in sync with production via
// code review; not imported from production code because fixture packages must
// be dependency-free (archtest_fixture build tag).
const fixtureMaxAge = 604800 // 7d seconds

// weakCookie intentionally weakens Secure. HttpOnly and SameSite are kept
// correct so the fixture exercises exactly one missing-attribute branch (the
// expected-count assertion in the RED-fixture test pins this to 1).
func weakCookie() *http.Cookie {
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure // RED fixture: Secure:false is the negative case REFRESH-COOKIE-SECURE-ATTRS-01 must catch
	return &http.Cookie{
		Name:     "gocell_rt",
		Value:    "x",
		Path:     "/api/v1/access/sessions",
		MaxAge:   fixtureMaxAge,
		HttpOnly: true,
		Secure:   false, // RED: weakened — must be caught
		SameSite: http.SameSiteStrictMode,
	}
}
