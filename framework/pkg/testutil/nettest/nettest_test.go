package nettest

import (
	"io"
	"net/http"
	"testing"
)

// helloHandler is the trivial body shared by both server smoke tests.
func helloHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
}

// fetch issues a GET against url using client and returns the body, failing
// the test on any transport or read error.
func fetch(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

// TestNewServer_ServesAndIsAddressable asserts the funnel returns a started,
// reachable server. In a TCP-less sandbox the call skips (the whole point of
// the funnel); under CI it binds and the assertions run.
func TestNewServer_ServesAndIsAddressable(t *testing.T) {
	srv := NewServer(t, helloHandler())
	defer srv.Close()

	if srv.URL == "" {
		t.Fatal("NewServer returned a server with an empty URL")
	}
	if got := fetch(t, srv.Client(), srv.URL); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

// TestNewTLSServer_ServesOverTLS asserts the TLS funnel returns a started TLS
// server whose own client trusts it. Same sandbox-skip contract as NewServer.
func TestNewTLSServer_ServesOverTLS(t *testing.T) {
	srv := NewTLSServer(t, helloHandler())
	defer srv.Close()

	if srv.URL == "" {
		t.Fatal("NewTLSServer returned a server with an empty URL")
	}
	if got := fetch(t, srv.Client(), srv.URL); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}
