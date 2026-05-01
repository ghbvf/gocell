package bootstrap

import (
	"net"
	"net/http"
	"testing"
)

// closeListener closes a net.Listener and logs any close error.
func closeListener(t *testing.T, ln net.Listener) {
	t.Helper()
	if err := ln.Close(); err != nil {
		t.Logf("close listener: %v", err)
	}
}

// closeBody closes resp.Body and logs any close error.
// Use in place of bare resp.Body.Close() to satisfy errcheck.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Logf("close body: %v", err)
	}
}

// closeConn closes a net.Conn and logs any close error.
func closeConn(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.Close(); err != nil {
		t.Logf("close conn: %v", err)
	}
}
