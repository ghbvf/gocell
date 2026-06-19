package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
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
// verify.smoke.enrollcell.startup target): it drives the full run() path — opt-in
// gate → composition build → serve — on pre-bound loopback listeners and asserts
// /healthz + /readyz are green. Going through run (not buildApp directly) covers the
// runnable wrapper too; a successful boot also proves composition.Build succeeded.
func TestRun_HealthReadyzGreen(t *testing.T) {
	t.Setenv(demoOptInEnv, "1") // run() is fail-closed without the explicit opt-in.

	// Pre-bind the listeners and hand them to bootstrap via WithListenerNet (no
	// listen→close→rebind window) so /healthz can be polled on known ports. bootstrap
	// owns + closes them on shutdown, so the test must not close them itself.
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

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, addrs, prebuiltListeners{
			primary:  primaryLn,
			internal: internalLn,
			health:   healthLn,
		})
	}()

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
			t.Fatalf("run returned unexpected error: %v", err)
		}
	case <-time.After(shutdownTimeout):
		t.Fatalf("app did not shut down within %s of context cancel", shutdownTimeout)
	}
}

// TestDefaultAddrs asserts every default listener binds loopback: even behind the
// MDMD_DEMO opt-in the demo daemon is never exposed on an external interface (defense
// in depth). MDM-PR15 sets the real device-facing primary bind.
func TestDefaultAddrs(t *testing.T) {
	addrs := defaultAddrs()
	for name, addr := range map[string]string{
		"primary":  addrs.primary,
		"internal": addrs.internal,
		"health":   addrs.health,
	} {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("%s addr %q is not host:port: %v", name, addr, err)
		}
		if host != "127.0.0.1" {
			t.Errorf("%s listener binds %q, want loopback 127.0.0.1 (demo must not be externally exposed)", name, host)
		}
	}
}

// TestRun_RefusesWithoutDemoOptIn asserts run is fail-closed: without MDMD_DEMO=1 it
// returns an error naming the opt-in and never builds/serves the app (so defaultAddrs
// is never bound). A warn banner is not a substitute for an explicit opt-in.
func TestRun_RefusesWithoutDemoOptIn(t *testing.T) {
	t.Setenv(demoOptInEnv, "0") // any value other than "1" must refuse.

	err := run(context.Background(), defaultAddrs(), prebuiltListeners{})
	if err == nil {
		t.Fatal("run() returned nil without MDMD_DEMO=1, want a fail-closed refusal")
	}
	if !strings.Contains(err.Error(), demoOptInEnv) {
		t.Errorf("refusal error %q does not name the opt-in env %q", err, demoOptInEnv)
	}
}

// TestNetListenerOpt covers both arms of the listener-injection seam: nil → no
// options (production lets bootstrap bind the addr itself); non-nil → exactly one
// WithListenerNet option (the smoke test's pre-bound path).
func TestNetListenerOpt(t *testing.T) {
	if got := netListenerOpt(nil); got != nil {
		t.Errorf("netListenerOpt(nil) = %v, want nil (bootstrap binds the addr itself)", got)
	}

	ln := mustLoopbackListener(t)
	defer func() { _ = ln.Close() }()
	if got := netListenerOpt(ln); len(got) != 1 {
		t.Errorf("netListenerOpt(non-nil) returned %d options, want 1 (WithListenerNet)", len(got))
	}
}

// TestBuildApp_InvalidAddrsRejected asserts the composition root is fail-fast on a
// bad bind config: empty addrs make composition.NewSharedDeps' health-reachability
// validation fail, and buildApp propagates that as an error rather than booting a
// misconfigured daemon. Covers buildMemSharedDeps' NewSharedDeps error arm + buildApp's
// error propagation.
func TestBuildApp_InvalidAddrsRejected(t *testing.T) {
	_, err := buildApp(context.Background(), listenerAddrs{}, prebuiltListeners{})
	if err == nil {
		t.Fatal("buildApp with empty addrs returned nil error, want a config validation failure")
	}
}

// TestRun_PropagatesBuildError asserts run surfaces a build failure (rather than
// panicking or booting half-built): with the opt-in satisfied but an invalid bind
// config, run returns buildApp's error wrapped with its "build app" context. Covers
// run's build-error arm (the gate-pass path that TestRun_RefusesWithoutDemoOptIn does not).
func TestRun_PropagatesBuildError(t *testing.T) {
	t.Setenv(demoOptInEnv, "1")

	err := run(context.Background(), listenerAddrs{}, prebuiltListeners{})
	if err == nil {
		t.Fatal("run() with opt-in but invalid addrs returned nil, want a wrapped build error")
	}
	if !strings.Contains(err.Error(), "build app") {
		t.Errorf("error %q missing run's build-app context", err)
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
