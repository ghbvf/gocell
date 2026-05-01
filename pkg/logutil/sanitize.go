// Package logutil provides shared helpers for safely emitting user-controlled
// or network-derived strings through structured logging (slog).
//
// slog's JSONHandler quotes string attribute values, so embedded control
// characters cannot break the wire format on its own. These helpers
// nevertheless strip control characters at the call site for two reasons:
//   - Defense in depth: when downstream pipelines (fluentd, journald,
//     stdout-tail consumers) interpret the rendered log line as text,
//     embedded ESC / BEL / NUL bytes can fool a parser even though slog
//     itself escaped them.
//   - Operator UX: rendered logs read by humans should not be polluted by
//     terminal control sequences from a hostile request.
package logutil

import (
	"net"
	"strings"
)

// Sanitize returns s with ASCII control characters (codepoint < 0x20 or
// equal to 0x7f) removed. Non-ASCII characters (including printable
// Unicode like emoji) are preserved.
//
// Use this on every user-controlled string that flows into a slog attribute,
// e.g. r.Method, r.URL.Path, r.Header.Get(...), env values.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// SafeAddr normalizes a network address string for structured-log emission.
// On success (host:port parses) it returns net.JoinHostPort(host, port),
// which round-trips equivalently for IPv4 / IPv6 / hostname forms. On
// parse failure (e.g. unix-socket "@" prefix, malformed peer address) it
// falls back to Sanitize so any control characters are stripped before
// the value reaches the logger.
func SafeAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Sanitize(addr)
	}
	return net.JoinHostPort(host, port)
}
