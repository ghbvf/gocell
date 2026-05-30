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
	"time"
)

// ── A1 violations: every net package dial-family function in the banned set ──
// One callsite per banned func so the reverse self-test asserts each is caught
// (a regression deleting any one entry from the banned set would fail the ≥6
// assertion).
func dialViaNetDial(ctx context.Context) (net.Conn, error) {
	return net.Dial("tcp", "10.0.0.1:80") // VIOLATION A1 (net.Dial)
}

func dialViaDialTCP() (net.Conn, error) {
	return net.DialTCP("tcp", nil, &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80}) // VIOLATION A1 (net.DialTCP)
}

func dialViaDialUDP() (net.Conn, error) {
	return net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80}) // VIOLATION A1 (net.DialUDP)
}

func dialViaDialIP() (net.Conn, error) {
	return net.DialIP("ip4:icmp", nil, &net.IPAddr{IP: net.ParseIP("10.0.0.1")}) // VIOLATION A1 (net.DialIP)
}

func dialViaDialUnix() (net.Conn, error) {
	return net.DialUnix("unix", nil, &net.UnixAddr{Name: "/tmp/x.sock", Net: "unix"}) // VIOLATION A1 (net.DialUnix)
}

func dialViaDialTimeout() (net.Conn, error) {
	return net.DialTimeout("tcp", "10.0.0.1:80", time.Second) // VIOLATION A1 (net.DialTimeout)
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
