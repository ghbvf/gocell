//go:build archtest_fixture

// Package headerreadfixture is an archtest RED fixture for
// HTTP-REQUEST-HEADER-READ-FUNNEL-01 (issue #1494).
//
// The funnel bans business code (cells/, examples/) from reading an INBOUND
// request header directly: every inbound business header must be declared in
// contract.yaml endpoints.http.headers and consumed via the generated Request
// field (the generated handler is the sole sanctioned reader). This fixture
// proves the detector fires on the read forms and does NOT false-positive on
// legitimate writes / response-header reads:
//
//   - RED (must be flagged): r.Header.Get / r.Header.Values / r.Header[...] on an
//     inbound *http.Request — 3 sites.
//   - GREEN (must NOT be flagged): req.Header.Set on an OUTBOUND request the caller
//     constructs (a write, e.g. Authorization), and w.Header().Get on a
//     ResponseWriter (the receiver is a method call, not a *http.Request.Header
//     field selector).
package headerreadfixture

import "net/http"

// badInboundGet is RED: reading an inbound request-header value directly,
// bypassing the contract.yaml headers: declaration + generated Request field.
func badInboundGet(r *http.Request) string {
	return r.Header.Get("X-Foo")
}

// badInboundValues is RED: the multi-value read API on the inbound request header.
func badInboundValues(r *http.Request) []string {
	return r.Header.Values("X-Foo")
}

// badInboundIndex is RED: indexing the inbound request-header map directly.
func badInboundIndex(r *http.Request) []string {
	return r.Header["X-Foo"]
}

// goodOutboundSet is GREEN: setting a header on an OUTBOUND request the caller
// constructs to call another service (e.g. Authorization) is a write, not an
// inbound read — must NOT be flagged.
func goodOutboundSet(req *http.Request) {
	req.Header.Set("Authorization", "ServiceToken x")
}

// goodResponseHeaderGet is GREEN: reading a response header being built via
// w.Header() — the receiver is a method call, not a *http.Request.Header field —
// must NOT be flagged.
func goodResponseHeaderGet(w http.ResponseWriter) string {
	return w.Header().Get("Content-Type")
}
