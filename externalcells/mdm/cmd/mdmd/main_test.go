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

// startupPollTimeout / shutdownTimeout are deliberately generous: a demo cold start
// generates an RSA key pair, and CI runs under CPU contention, so a tight 5s budget
// risks false flakes. The app shuts down far faster in practice.
const (
	startupPollTimeout = 20 * time.Second
	shutdownTimeout    = 15 * time.Second
)

// TestRun_HealthReadyzGreen is the demo-topology startup smoke test (the
// verify.smoke.enrollcell.startup target): it builds the full composition, boots it
// on pre-bound loopback listeners, and asserts /healthz + /readyz are green. A
// successful boot also proves composition.Build succeeded, so a separate wiring-only
// test would be redundant.
func TestRun_HealthReadyzGreen(t *testing.T) {
	// Pre-bind the listeners and hand them to bootstrap via WithListenerNet (no
	// listen→close→rebind window). bootstrap owns + closes them on shutdown, so the
	// test must not close them itself.
	primaryLn := mustLoopbackListener(t)
	internalLn := mustLoopbackListener(t)
	healthLn := mustLoopbackListener(t)

	addrs := listenerAddrs{
		primary:  primaryLn.Addr().String(),
		internal: internalLn.Addr().String(),
		health:   healthLn.Addr().String(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := buildApp(ctx, addrs, prebuiltListeners{
		primary:  primaryLn,
		internal: internalLn,
		health:   healthLn,
	})
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(ctx) }()

	deadline := time.Now().Add(startupPollTimeout)
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
	case <-time.After(shutdownTimeout):
		t.Fatalf("app did not shut down within %s of context cancel", shutdownTimeout)
	}
}

// mustLoopbackListener binds an OS-assigned free loopback port and keeps the
// listener open for bootstrap to adopt (WithListenerNet) — zero rebind race.
func mustLoopbackListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback listener: %v", err)
	}
	return ln
}

// pollGreen GETs url until it returns 200 or the deadline passes.
func pollGreen(url string, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec,noctx // test polls a self-constructed loopback health URL, not attacker input
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
