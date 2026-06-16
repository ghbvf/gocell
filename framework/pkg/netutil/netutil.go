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
// This is a SYNTACTIC check only. Production transport security (TLS/mTLS,
// non-loopback enforcement) for remote endpoints is enforced separately by
// US6 #1964 per the ADR security-gap matrix (docs/architecture/
// 202606131142-1423-adr-cell-deployment-topology.md). Do NOT add TLS or
// loopback rejection here — that belongs to US6. Loopback (localhost) IS
// accepted: legitimate for dev/compose/multi-process-test topologies.
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
