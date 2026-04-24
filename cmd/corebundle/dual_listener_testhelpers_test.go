package main

import (
	"net"
	"testing"
)

// newCorebundleLocalListener returns an ephemeral TCP listener bound to
// 127.0.0.1:0 for tests that need to inject an internal listener alongside
// a primary test listener. Mirrors bootstrap_test.newLocalListener but lives
// in the corebundle package to avoid a bootstrap test-helper export.
func newCorebundleLocalListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create test listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
