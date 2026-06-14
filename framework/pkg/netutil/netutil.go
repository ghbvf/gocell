// Package netutil provides shared network address utilities for the GoCell framework.
package netutil

import (
	"net"
	"net/url"
	"strings"
)

// IsValidNetworkAddress reports whether ep is a syntactically valid cell
// transport endpoint: a bare "host:port" (net.SplitHostPort) OR an http/https
// URL with a non-empty host. Other schemes (file/gopher/…) are rejected.
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
		return true
	}
	// Try host:port (covers "host:8080", "[::1]:9000", etc.).
	// Reject when the "port" part contains a slash — that indicates a URL-like
	// string that net.SplitHostPort parsed erroneously (e.g. "file:///tmp/sock"
	// splits as host="file", port="//tmp/sock").
	if host, port, err := net.SplitHostPort(ep); err == nil &&
		host != "" && !strings.Contains(port, "/") {
		return true
	}
	return false
}
