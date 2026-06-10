//go:build archtest_fixture

// Package refreshcookiefixture holds RED fixtures for the refresh-cookie
// archtests:
//
//   - REFRESH-COOKIE-SECURE-ATTRS-01: weakCookie sets Secure:false so the
//     production scan (scanRefreshCookieSecureAttrs) MUST report exactly one
//     diagnostic.
//   - REFRESH-COOKIE-SINGLE-WRITER-01: crossWriterCookie writes the refresh
//     cookie name OUTSIDE the httpcookie package so the cross-package scan
//     (scanRefreshCookieSingleWriter) MUST report exactly one diagnostic.
//
// Without these fixtures each rule's zero-diagnostic outcome on real code would
// carry no information (rule works AND no violation, vs rule silently broken).
// Gated by the archtest_fixture build tag; never compiled in normal builds.
package refreshcookiefixture

import "net/http"

// fixtureMaxAge mirrors accesscore.DefaultRefreshMaxAge (7 days in seconds).
// Named to avoid bare magic literals and to stay in sync with production via
// code review; not imported from production code because fixture packages must
// be dependency-free (archtest_fixture build tag).
const fixtureMaxAge = 604800 // 7d seconds

// weakCookie intentionally weakens Secure. HttpOnly and SameSite are kept
// correct so the fixture exercises exactly one missing-attribute branch (the
// expected-count assertion in the RED-fixture test pins this to 1). Its name is
// deliberately NOT the refresh sentinel, so it does not also trip
// REFRESH-COOKIE-SINGLE-WRITER-01.
func weakCookie() *http.Cookie {
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure // RED fixture: Secure:false is the negative case REFRESH-COOKIE-SECURE-ATTRS-01 must catch
	return &http.Cookie{
		Name:     "fixture_rt",
		Value:    "x",
		Path:     "/",
		MaxAge:   fixtureMaxAge,
		HttpOnly: true,
		Secure:   false, // RED: weakened — must be caught
		SameSite: http.SameSiteStrictMode,
	}
}

// crossWriterCookie writes the refresh-cookie name "__Host-gocell_rt" from a
// package OTHER than corecells/accesscore/internal/httpcookie. All security
// attributes are correct (so it does NOT trip REFRESH-COOKIE-SECURE-ATTRS-01);
// the single-writer scan must catch the out-of-package sentinel name. This is
// the negative case REFRESH-COOKIE-SINGLE-WRITER-01 must catch.
func crossWriterCookie() *http.Cookie {
	return &http.Cookie{
		Name:     "__Host-gocell_rt", // RED: refresh cookie written outside httpcookie
		Value:    "x",
		Path:     "/",
		MaxAge:   fixtureMaxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}
