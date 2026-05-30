// Package webhook_ssrf_violate is a synthetic violation fixture for the
// WEBHOOK-SSRF-GUARD-01 archtest rule. Each violation form is exercised once so
// the reverse self-test (TestWebhookSSRFGuard_ReverseFixture) can assert the
// corresponding sub-rule fires.
//
// DO NOT use this package in production code.
package webhook_ssrf_violate

import (
	"context"
	"net"
	"net/http"
)

// ── A1 violations: net package dial-family functions ─────────────────────────
func dialViaNetDial(ctx context.Context) (net.Conn, error) {
	return net.Dial("tcp", "10.0.0.1:80") // VIOLATION A1 (net.Dial)
}

func dialViaDialTCP() (net.Conn, error) {
	return net.DialTCP("tcp", nil, &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80}) // VIOLATION A1 (net.DialTCP)
}

// ── A2 violation: raw net.Dialer.DialContext outside ssrf.go dial() ──────────
func dialViaRawDialer(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{}
	return d.DialContext(ctx, "tcp", "10.0.0.1:80") // VIOLATION A2 (un-vetted dialer)
}

// ── A3 violations: net/http global client / transport / convenience funcs ────
func fetchViaDefaultClient(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req) // VIOLATION A3 (http.DefaultClient)
}

func fetchViaGet() (*http.Response, error) {
	return http.Get("http://10.0.0.1/") // VIOLATION A3 (http.Get)
}

func transportRef() http.RoundTripper {
	return http.DefaultTransport // VIOLATION A3 (http.DefaultTransport)
}
