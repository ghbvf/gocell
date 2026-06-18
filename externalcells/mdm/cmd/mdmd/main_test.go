package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestRun_HealthReadyzGreen is the demo-topology startup smoke test (the
// verify.smoke.enrollcell.startup target): it builds the full composition,
// boots it on OS-assigned loopback ports, and asserts /healthz + /readyz are
// green. A successful boot also proves composition.Build succeeded, so a
// separate wiring-only test would be redundant.
func TestRun_HealthReadyzGreen(t *testing.T) {
	addrs := listenerAddrs{
		primary:  freeLoopbackAddr(t),
		internal: freeLoopbackAddr(t),
		health:   freeLoopbackAddr(t),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := buildApp(ctx, addrs)
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for _, path := range []string{"/healthz", "/readyz"} {
		url := "http://" + addrs.health + path
		if !pollGreen(url, deadline) {
			t.Fatalf("endpoint never became green: %s", url)
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("app.Run returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app did not shut down within 5s of context cancel")
	}
}

// freeLoopbackAddr grabs an OS-assigned free loopback port and releases it so the
// app can bind it — deterministic vs a hard-coded port that may collide in CI.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free loopback port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return addr
}

// pollGreen GETs url until it returns 200 or the deadline passes.
func pollGreen(url string, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // test poll loop
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
