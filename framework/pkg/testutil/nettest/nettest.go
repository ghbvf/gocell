// Package nettest is the sanctioned funnel for httptest.NewServer,
// httptest.NewTLSServer and httptest.NewUnstartedServer in packages that are
// locked by the SANDBOX-HTTPTEST-TCP-FUNNEL-01 archtest.
//
// # Motivation
//
// CI/agent sandboxes forbid net.Listen, which causes bare httptest.NewServer(h),
// httptest.NewTLSServer(h) and httptest.NewUnstartedServer(h) calls to panic —
// all three bind a loopback listener in their constructor via net.Listen.
// Replacing bare httptest.New* calls with the helpers in this package gives
// tests a clean t.Skip path instead of a panic.
//
// # Sanctioned funnel
//
// The archtest rule SANDBOX-HTTPTEST-TCP-FUNNEL-01 (Medium) bans bare
// httptest.NewServer, httptest.NewTLSServer and httptest.NewUnstartedServer call
// sites in the adapters/websocket and adapters/oidc packages; callers in those
// packages MUST use NewServer, NewTLSServer or NewUnstartedServer from this
// package instead.
//
// nettest itself is not in the scan scope, so it may call httptest directly
// without triggering the rule. Packages outside the current scan scope are
// free to call httptest directly until the scope is widened in a follow-up
// issue.
//
// # Probe-before-resource ordering
//
// RequireTCP is exported so a test that starts an un-cleaned-up resource (e.g. a
// Hub goroutine) BEFORE creating the test server can probe TCP up front and skip
// before allocating anything. A t.Skipf raised inside NewServer AFTER such a
// resource is started would otherwise leak it — testing.SkipNow stops only the
// test goroutine, not the others it spawned, and cleanup registered after the
// skip-able call never runs. Call RequireTCP(t) before starting such resources.
//
// # Build tag
//
// This package carries no build tag and compiles unconditionally, so it may be
// imported by production test helpers without any tag constraint on the caller.
//
// # Unconditional-skip analyzer note
//
// The skip inside RequireTCP is guarded by an if-err-not-nil conditional, so it
// is never an unconditional first statement at the call site. The
// unconditionalskip analyzer will not flag RequireTCP, NewServer, NewTLSServer
// or NewUnstartedServer. (Mirror of the docker.go pattern in
// tests/e2e/internal/require.)
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
	RequireTCP(t)
	return httptest.NewServer(handler)
}

// NewTLSServer probes TCP availability, skips the test if the sandbox cannot
// bind, and otherwise returns a started TLS *httptest.Server backed by
// handler.
//
// Same sanctioned-funnel contract as NewServer.
func NewTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	RequireTCP(t)
	return httptest.NewTLSServer(handler)
}

// NewUnstartedServer probes TCP availability, skips the test if the sandbox
// cannot bind, and otherwise returns an UNstarted *httptest.Server backed by
// handler; the caller starts it with Start or StartTLS.
//
// httptest.NewUnstartedServer binds the loopback listener in its constructor
// (not at Start), so the TCP probe must happen here. Same sanctioned-funnel
// contract as NewServer.
func NewUnstartedServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	RequireTCP(t)
	return httptest.NewUnstartedServer(handler)
}

// RequireTCP skips t if TCP listening on loopback is not permitted (e.g. a
// CI/agent sandbox with net.Listen disabled). Call it BEFORE allocating any
// resource that must be cleaned up so the skip path does not leak that resource;
// NewServer/NewTLSServer/NewUnstartedServer call it internally.
//
// The skip is inside an if-err branch so the unconditionalskip analyzer does
// not flag this helper.
func RequireTCP(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("nettest: cannot listen on TCP (sandbox?): %v", err)
	}
	_ = ln.Close()
}
