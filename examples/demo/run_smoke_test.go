package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// Wall-clock budgets for the boot+serve smoke (extracted per TEST-TIME-LITERAL-01).
const (
	// smokeServeDeadline bounds how long the test polls for the hello endpoint to
	// start serving. Boot is millisecond-scale (a single in-memory L1 cell + two
	// loopback listeners); the deadline only needs a wide margin over boot time.
	smokeServeDeadline = 5 * time.Second
	// smokePollInterval is the gap between readiness polls while the app boots.
	smokePollInterval = 20 * time.Millisecond
	// smokeHTTPTimeout bounds each individual HTTP request.
	smokeHTTPTimeout = 2 * time.Second
	// smokeShutdownGrace bounds how long the test waits for app.Run to return
	// after ctx cancel before declaring it wedged (a hang backstop, not a sleep).
	smokeShutdownGrace = 10 * time.Second
)

// TestDemoBootstrapServesHello is the end-to-end smoke for the demo example. It
// boots the real run.go wiring (buildDemoBootstrap) on pre-bound ephemeral
// loopback listeners and issues real HTTP requests through the full
// listener → router → auth → RouteGroup → handler path:
//
//   - GET /api/v1/hello returns 200 with the fixed JSON greeting;
//   - GET /healthz and /readyz return 200.
//
// Unlike the slice contract_test.go (which calls the generated handler in
// isolation via httptest.NewRecorder), this asserts that the +slice:route marker
// → cell_gen.go RouteGroup prefix/subPath → mounted mux actually serves the
// user-facing path. A route-mounting regression that still boots cleanly — which
// a bare boot smoke would miss — is caught here. Pre-bound listeners (injected
// via WithListenerNet) give the test the bound ports without needing a
// bound-address accessor on *bootstrap.Bootstrap, and keep it collision-free.
func TestDemoBootstrapServesHello(t *testing.T) {
	t.Parallel()

	primaryLn := mustLoopbackListener(t)
	healthLn := mustLoopbackListener(t)
	primaryURL := "http://" + primaryLn.Addr().String()
	healthURL := "http://" + healthLn.Addr().String()

	noAuth := []kauth.ListenerAuth{kauth.AuthNone{}}
	app, err := buildDemoBootstrap(
		"demo", []string{"democell"},
		bootstrap.WithListener(cell.PrimaryListener, "", noAuth, bootstrap.WithListenerNet(primaryLn)),
		bootstrap.WithListener(cell.HealthListener, "", noAuth, bootstrap.WithListenerNet(healthLn)),
	)
	require.NoError(t, err, "buildDemoBootstrap must assemble without error")
	require.NotNil(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- app.Run(ctx) }()

	// Poll the hello endpoint until the app is serving, then assert the body.
	body := pollHelloUntilServing(t, primaryURL+"/api/v1/hello")
	require.JSONEq(t, `{"data":{"message":"hello, gocell"}}`, body)

	// /healthz and /readyz must also serve 200 (readyz aggregates over 0 probes).
	require.Equal(t, http.StatusOK, getStatusCode(t, healthURL+"/healthz"))
	require.Equal(t, http.StatusOK, getStatusCode(t, healthURL+"/readyz"))

	// Graceful shutdown: cancel the context and assert a clean (context) return.
	cancel()
	select {
	case runErr := <-runErrCh:
		if runErr != nil &&
			!errors.Is(runErr, context.Canceled) &&
			!errors.Is(runErr, context.DeadlineExceeded) {
			t.Fatalf("demo app.Run returned a non-context error on shutdown: %v", runErr)
		}
	case <-time.After(smokeShutdownGrace):
		t.Fatal("app.Run did not return within the shutdown grace after ctx cancel")
	}
}

// mustLoopbackListener binds an ephemeral loopback TCP listener for injection
// into bootstrap via WithListenerNet (so the test knows the bound port).
func mustLoopbackListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "bind ephemeral loopback listener")
	t.Cleanup(func() { _ = ln.Close() }) // bootstrap also closes it on shutdown; double-close is harmless
	return ln
}

// pollHelloUntilServing polls url until it returns 200 (the app boots
// asynchronously) and returns the response body, or fails after the deadline.
func pollHelloUntilServing(t *testing.T, url string) string {
	t.Helper()
	pollCtx, cancel := context.WithTimeout(context.Background(), smokeServeDeadline)
	defer cancel()
	for {
		if body, ok := tryGet200(pollCtx, url); ok {
			return body
		}
		select {
		case <-pollCtx.Done():
			t.Fatalf("GET %s did not return 200 within %v", url, smokeServeDeadline)
			return ""
		case <-time.After(smokePollInterval):
		}
	}
}

// tryGet200 issues one GET; returns (body, true) iff the response is 200.
func tryGet200(ctx context.Context, url string) (string, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, smokeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false
	}
	return string(body), true
}

// getStatusCode issues a single GET and returns the status code.
func getStatusCode(t *testing.T, url string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "GET %s", url)
	_ = resp.Body.Close()
	return resp.StatusCode
}
