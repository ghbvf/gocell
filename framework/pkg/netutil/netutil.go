// Package netutil provides shared network address utilities for the GoCell framework.
package netutil

import (
	"net"
	"net/url"
	"strings"
)

// IsValidNetworkAddress reports whether ep is a syntactically valid cell
// transport endpoint: a bare "host:port" (net.SplitHostPort) OR an http/https
// URL with a non-empty host and no path (beyond an optional root "/"), query, or
// fragment. Other schemes (file/gopher/…) are rejected.
//
// A cell endpoint is scheme+host[:port] only. The remote transport rewrites a
// request's Scheme+Host to the endpoint and preserves the request's own
// path/query, so any path/query/fragment ON the endpoint would be silently
// dropped — reject it here (fail-closed, no silent truncate) rather than accept
// a misconfiguration that loses part of the address (#1966 review P2.9). A bare
// trailing "/" is allowed (a common, information-free operator form).
//
// This is a SYNTACTIC check only. Production transport security (mTLS peer
// authentication + non-loopback enforcement) for remote endpoints is enforced
// separately (#2263 per the ADR security-gap matrix, docs/architecture/
// 202606131142-1423-adr-cell-deployment-topology.md): see [IsLoopbackEndpoint]
// for the loopback dividing line and cellmodules/celltransport for the
// fail-closed mTLS gate. Do NOT fold TLS or loopback rejection INTO this
// syntactic check — loopback (localhost) IS a valid address here, legitimate for
// dev/compose/multi-process-test topologies.
func IsValidNetworkAddress(ep string) bool {
	// Try http/https URL first (unambiguous because of the scheme prefix).
	// Must be checked before SplitHostPort to avoid "http" being treated as host.
	if u, err := url.Parse(ep); err == nil && u.Host != "" &&
		(u.Scheme == "http" || u.Scheme == "https") {
		// scheme+host[:port] only — no path (beyond an optional root "/"), query,
		// or fragment (see godoc).
		return (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
	}
	// Try host:port (covers "host:8080", "[::1]:9000", etc.).
	// Reject when the "port" part contains a slash — that indicates a URL-like
	// string that net.SplitHostPort parsed erroneously (e.g. "file:///tmp/sock"
	// splits as host="file", port="//tmp/sock") — or any path/query/fragment on
	// a bare host:port (e.g. "host:8080/foo").
	if host, port, err := net.SplitHostPort(ep); err == nil &&
		host != "" && !strings.ContainsAny(port, "/?#") {
		return true
	}
	return false
}

// IsLoopbackEndpoint reports whether ep's host is a loopback address: an IP in
// 127.0.0.0/8, ::1, or the literal "localhost". It accepts the same endpoint
// forms as [IsValidNetworkAddress] (bare host:port or http/https URL). A
// syntactically invalid endpoint returns false.
//
// This is the dividing line for split-topology mTLS enforcement (#2263): a
// loopback peer is a local dev / multi-process-test deployment where plaintext
// is acceptable, whereas a non-loopback peer crosses a real network boundary and
// MUST use mTLS (fail-closed). It is the "non-loopback enforcement" that
// IsValidNetworkAddress's godoc defers.
func IsLoopbackEndpoint(ep string) bool {
	host, ok := endpointHost(ep)
	if !ok {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// endpointHost extracts the host (no port, no brackets) from a cell transport
// endpoint in either accepted form. ok is false for a syntactically invalid
// endpoint. Mirrors the form-acceptance of IsValidNetworkAddress so the two
// agree on what counts as a valid endpoint.
func endpointHost(ep string) (host string, ok bool) {
	if u, err := url.Parse(ep); err == nil && u.Host != "" &&
		(u.Scheme == "http" || u.Scheme == "https") {
		return u.Hostname(), true
	}
	if h, port, err := net.SplitHostPort(ep); err == nil &&
		h != "" && !strings.ContainsAny(port, "/?#") {
		return h, true
	}
	return "", false
}
