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
// Note: "unix" is NOT in this map — unix sockets are handled separately via
// isUnixScheme so that a host component (e.g. unix://evil.host/x) is rejected.
var tlsSchemes = map[string]bool{
	"https":       true,
	"rediss":      true,
	"tls":         true,
	"wss":         true,
	"vault+https": true,
}

// nonTLSSchemes is the set of URL schemes that are plaintext but may be
// accepted for loopback IP literals (dev/CI testcontainer exception).
var nonTLSSchemes = map[string]bool{
	"http":  true,
	"redis": true,
	"ws":    true,
	"tcp":   true,
	"vault": true,
}

// isLoopbackIPLiteral reports whether host (port already stripped) is a
// loopback IP literal. DNS names, including "localhost", are intentionally
// rejected for non-TLS schemes so the dev/CI exception cannot be widened by
// resolver behavior or host-file changes.
func isLoopbackIPLiteral(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
// a loopback-IP-literal exception for dev/CI testcontainers. Accepts:
//
//   - URL form (scheme://host[:port][/path]): scheme must be one of
//     {https, rediss, tls, wss, vault+https}; or unix:// with an empty host
//     (local socket path only).
//   - Bare host:port form: host must be a loopback IP literal (127.x.x.x,
//     ::1, or IPv4-mapped IPv6), OR fail-closed. The string "localhost" is
//     not accepted for non-TLS endpoints.
//
// Returns errcode.ErrAdapterEndpointNotTLS-tagged error otherwise.
//
// ref: docs/plans/202604270020-1-2-ci-3-claude-ship-reactive-bachman.md PR-MODE-1
func ValidateTLSEndpoint(raw string) error {
	if raw == "" {
		return errcode.New(errcode.KindInternal, errcode.ErrAdapterEndpointNotTLS,
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
		// url.Parse rarely errors; use a fixed redacted placeholder to avoid
		// emitting the raw string (which may carry userinfo credentials).
		return errcode.Wrap(errcode.KindInternal, errcode.ErrAdapterEndpointNotTLS,
			"adapter endpoint: cannot parse URL (redacted: may contain credentials)", err)
	}

	scheme := strings.ToLower(u.Scheme)

	// unix:// is accepted only when the host component is empty (local socket).
	// unix://evil.host/x would silently route to a remote host without TLS, so
	// it is rejected. unix:///var/run/redis.sock (host="") is accepted.
	if scheme == "unix" {
		if u.Host != "" {
			return errcode.New(errcode.KindInternal, errcode.ErrAdapterEndpointNotTLS,
				"adapter endpoint: unix:// scheme requires an empty host (local socket only)",
				errcode.WithInternal(fmt.Sprintf("host=%q", u.Host)))
		}
		return nil
	}

	// TLS schemes are always accepted regardless of host.
	if tlsSchemes[scheme] {
		return nil
	}

	// Non-TLS schemes are accepted only for loopback IP literals.
	if nonTLSSchemes[scheme] {
		host := extractHost(u.Host)
		if isLoopbackIPLiteral(host) {
			return nil
		}
		return errcode.New(errcode.KindInternal, errcode.ErrAdapterEndpointNotTLS,
			"adapter endpoint: non-TLS scheme used for remote host",
			errcode.WithInternal(fmt.Sprintf("url=%s scheme=%q host=%q", u.Redacted(), scheme, host)))
	}

	// Unknown scheme — fail closed.
	return errcode.New(errcode.KindInternal, errcode.ErrAdapterEndpointNotTLS,
		"adapter endpoint: unrecognized scheme",
		errcode.WithInternal(fmt.Sprintf("url=%s scheme=%q", u.Redacted(), scheme)))
}

// validateBareHostPort handles strings without "://" (bare host:port or host).
func validateBareHostPort(raw string) error {
	host := extractHost(raw)
	if isLoopbackIPLiteral(host) {
		return nil
	}
	return errcode.New(errcode.KindInternal, errcode.ErrAdapterEndpointNotTLS,
		"adapter endpoint: bare host:port is not a loopback IP literal and has no TLS scheme",
		errcode.WithInternal(fmt.Sprintf("addr=%q", raw)))
}
