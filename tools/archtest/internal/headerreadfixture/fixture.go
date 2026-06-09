//go:build archtest_fixture

// Package headerreadfixture is an archtest RED fixture for
// HTTP-REQUEST-HEADER-READ-FUNNEL-01 (issue #1494).
//
// The funnel bans business code (cells/, corecells/, examples/) from reading an INBOUND
// request header directly: every inbound business header must be declared in
// contract.yaml endpoints.http.headers and consumed via the generated Request
// field (the generated handler is the sole sanctioned reader). This fixture
// proves the detector fires on the read forms (including the one-hop alias) and
// does NOT false-positive on legitimate writes / response-header reads:
//
//   - RED (must be flagged): r.Header.Get / r.Header.Values / r.Header[...] on an
//     inbound *http.Request, PLUS the one-hop alias `h := r.Header; h.Get(...)`
//     (#1494 review F5) — 4 sites.
//   - GREEN (must NOT be flagged): req.Header.Set on an OUTBOUND request the caller
//     constructs (a write, e.g. Authorization); w.Header().Get on a ResponseWriter
//     (the receiver is a method call, not a *http.Request.Header field selector or
//     its alias); and an inter-procedural read where the header is passed to a
//     helper taking http.Header (the documented Medium residue — a genuine Go
//     static-analysis ceiling, not a cheap one-hop bypass).
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

// badAliasedHeaderRead is RED (#1494 review F5 — one-hop alias now closed): the
// header is bound to a local (`h := r.Header`) and read via `h.Get`. The detector
// tracks variables aliased to an inbound *http.Request.Header by go/types object
// identity, so this is flagged like a direct read. It is the 4th RED site.
func badAliasedHeaderRead(r *http.Request) string {
	h := r.Header
	return h.Get("X-Foo")
}

// goodInterProceduralRead is GREEN (documented Medium residue): the header is
// passed to a helper taking http.Header; inside the helper the receiver is a
// parameter not traceable to an *http.Request, so the read is NOT flagged. This
// is the genuine Go static-analysis ceiling (inter-procedural data flow), NOT a
// cheap one-hop bypass — it is the honest residue the read-funnel cannot close
// statically. The same-function one-hop alias (badAliasedHeaderRead) IS closed.
func goodInterProceduralRead(r *http.Request) string {
	return readFromHeader(r.Header)
}

func readFromHeader(h http.Header) string {
	return h.Get("X-Foo")
}
