// Package secutil provides security utility helpers shared across GoCell adapters.
package secutil

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// tlsSchemes is the set of URL schemes that are considered TLS-secured.
// Connections using these schemes are accepted for any host.
var tlsSchemes = map[string]bool{
	"https":       true,
	"rediss":      true,
	"tls":         true,
	"wss":         true,
	"unix":        true,
	"vault+https": true,
}

// nonTLSSchemes is the set of URL schemes that are plaintext but may be
// accepted for loopback hosts (dev/CI testcontainer exception).
var nonTLSSchemes = map[string]bool{
	"http":  true,
	"redis": true,
	"ws":    true,
	"tcp":   true,
	"vault": true,
}

// loopbackHosts is the canonical set of loopback hostnames and IP literals.
var loopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"::1":       true,
	"[::1]":     true,
	"localhost": true,
}

// isLoopback reports whether host (without port) is a loopback address.
// host must already have the port stripped.
func isLoopback(host string) bool {
	return loopbackHosts[host]
}

// extractHost returns the hostname component from a URL host field or a
// bare host:port string. For bare strings that look like IPv6 addresses
// (no "://") the entire string is treated as the host.
func extractHost(hostport string) string {
	// Handle IPv6 literals that may or may not carry a port.
	if strings.HasPrefix(hostport, "[") {
		// [::1]:port or [::1]
		h, _, err := net.SplitHostPort(hostport)
		if err != nil {
			// No port — strip brackets manually.
			return strings.Trim(hostport, "[]")
		}
		return h
	}
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port component — the whole string is the host.
		return hostport
	}
	return h
}

// ValidateTLSEndpoint validates that a remote endpoint addr enforces TLS, with
// a loopback exception for dev/CI testcontainers. Accepts:
//
//   - URL form (scheme://host[:port][/path]): scheme must be one of
//     {https, rediss, tls, wss, vault+https} (configurable list).
//   - Bare host:port form: host must be loopback (127.0.0.1, ::1, localhost),
//     OR fail-closed.
//
// Returns errcode.ErrAdapterEndpointNotTLS-tagged error otherwise.
//
// ref: docs/plans/202604270020-1-2-ci-3-claude-ship-reactive-bachman.md PR-MODE-1
func ValidateTLSEndpoint(raw string) error {
	if raw == "" {
		return errcode.New(errcode.ErrAdapterEndpointNotTLS,
			"adapter endpoint: empty endpoint is not TLS-secured")
	}

	if strings.Contains(raw, "://") {
		return validateURLForm(raw)
	}
	return validateBareHostPort(raw)
}

// validateURLForm handles strings that contain "://" and are parsed as URLs.
func validateURLForm(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errcode.Wrap(errcode.ErrAdapterEndpointNotTLS,
			fmt.Sprintf("adapter endpoint: cannot parse URL %q", raw), err)
	}

	scheme := strings.ToLower(u.Scheme)

	// TLS schemes are always accepted regardless of host.
	if tlsSchemes[scheme] {
		return nil
	}

	// Non-TLS schemes are accepted only for loopback hosts.
	if nonTLSSchemes[scheme] {
		host := extractHost(u.Host)
		if isLoopback(host) {
			return nil
		}
		return errcode.New(errcode.ErrAdapterEndpointNotTLS,
			fmt.Sprintf("adapter endpoint: %q uses non-TLS scheme %q for remote host %q; use a TLS scheme or a loopback address", raw, scheme, host))
	}

	// Unknown scheme — fail closed.
	return errcode.New(errcode.ErrAdapterEndpointNotTLS,
		fmt.Sprintf("adapter endpoint: %q has unrecognised scheme %q; expected a TLS scheme (https, rediss, tls, wss)", raw, scheme))
}

// validateBareHostPort handles strings without "://" (bare host:port or host).
func validateBareHostPort(raw string) error {
	host := extractHost(raw)
	if isLoopback(host) {
		return nil
	}
	return errcode.New(errcode.ErrAdapterEndpointNotTLS,
		fmt.Sprintf("adapter endpoint: bare host:port %q is not a loopback address and has no TLS scheme", raw))
}
