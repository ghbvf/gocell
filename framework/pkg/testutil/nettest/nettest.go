// Package nettest is the sanctioned funnel for httptest.NewServer and
// httptest.NewTLSServer in packages that are locked by the
// SANDBOX-HTTPTEST-TCP-FUNNEL-01 archtest.
//
// # Motivation
//
// CI/agent sandboxes forbid net.Listen, which causes bare
// httptest.NewServer(h) and httptest.NewTLSServer(h) calls to panic or
// hang (the constructor calls net.Listen("tcp", "127.0.0.1:0")
// unconditionally). Replacing bare httptest.New* calls with the helpers in
// this package gives tests a clean t.Skip path instead of a panic.
//
// # Sanctioned funnel
//
// The archtest rule SANDBOX-HTTPTEST-TCP-FUNNEL-01 (Medium) bans bare
// httptest.NewServer and httptest.NewTLSServer call sites in the
// adapters/websocket and adapters/oidc packages; callers in those packages
// MUST use NewServer and NewTLSServer from this package instead.
//
// nettest itself is not in the scan scope, so it may call httptest directly
// without triggering the rule. Packages outside the current scan scope are
// free to call httptest directly until the scope is widened in a follow-up
// issue.
//
// # Unconditional-skip analyzer note
//
// The skip inside skipIfNoTCP is guarded by an if-err-not-nil conditional,
// so it is never an unconditional first statement at the call site. The
// unconditionalskip analyzer will not flag NewServer or NewTLSServer.
// (Mirror of the docker.go pattern in tests/e2e/internal/require.)
package nettest

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NewServer probes TCP availability, skips the test if the sandbox cannot
// bind (e.g. CI/agent sandbox with net.Listen disabled), and otherwise
// returns a started *httptest.Server backed by handler.
//
// Callers in the adapters/websocket and adapters/oidc packages MUST use this
// function instead of httptest.NewServer directly; the archtest rule
// SANDBOX-HTTPTEST-TCP-FUNNEL-01 enforces this.
func NewServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	skipIfNoTCP(t)
	return httptest.NewServer(handler)
}

// NewTLSServer probes TCP availability, skips the test if the sandbox cannot
// bind, and otherwise returns a started TLS *httptest.Server backed by
// handler.
//
// Same sanctioned-funnel contract as NewServer.
func NewTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	skipIfNoTCP(t)
	return httptest.NewTLSServer(handler)
}

// skipIfNoTCP skips t if TCP listening on loopback is not permitted.
// The skip is inside an if-err branch so the unconditionalskip analyzer
// does not flag this helper.
func skipIfNoTCP(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("nettest: cannot listen on TCP (sandbox?): %v", err)
	}
	_ = ln.Close()
}
