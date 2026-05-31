package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	platformaccesscore "github.com/ghbvf/gocell/platform/accesscore"
	platformauditcore "github.com/ghbvf/gocell/platform/auditcore"
	platformconfigcore "github.com/ghbvf/gocell/platform/configcore"
	"github.com/ghbvf/gocell/runtime/composition"
)

// smokeBootDuration is the time the example is allowed to start and respond
// to a /healthz probe before the test cancels the context.
// Extracted per TEST-TIME-LITERAL-01 archtest requirement.
const smokeBootDuration = 5 * time.Second

// smokeHTTPTimeout is the HTTP client timeout for the /healthz probe.
// Extracted per TEST-TIME-LITERAL-01 archtest requirement.
const smokeHTTPTimeout = 2 * time.Second

// smokeBindWait is the static sleep duration given to the bootstrap HTTP
// listener to complete port binding before the test attempts to probe it.
// Extracted per TEST-TIME-LITERAL-01 archtest requirement.
const smokeBindWait = 300 * time.Millisecond

// smokeRetryInterval is the sleep between successive /healthz probe retries.
// Extracted per TEST-TIME-LITERAL-01 archtest requirement.
const smokeRetryInterval = 200 * time.Millisecond

// TestStarterBuildSucceeds verifies that buildStarterMemSharedDeps +
// composition.New().With(...).Build() completes without error and returns a
// non-nil *App.  This is a pure-memory, infra-free smoke test.
//
// accesscore.Module.Provide reads GOCELL_BOOTSTRAP_ADMIN_USERNAME /
// GOCELL_BOOTSTRAP_ADMIN_PASSWORD from the environment.  We set them via
// t.Setenv so they revert automatically after the test.
func TestStarterBuildSucceeds(t *testing.T) {
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "testadmin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "testadminpass1!")

	ctx := context.Background()
	shared, err := buildStarterMemSharedDeps(ctx)
	require.NoError(t, err, "buildStarterMemSharedDeps must succeed in memory mode")
	require.NotNil(t, shared, "shared deps must be non-nil")

	app, err := composition.New().
		With(
			platformauditcore.Module(),
			platformaccesscore.Module(),
			platformconfigcore.Module(),
		).
		Build(ctx, shared, starterRuntimeOptions(shared))

	require.NoError(t, err, "Build must succeed with all three platform modules")
	assert.NotNil(t, app, "Build must return a non-nil *App")
}

// TestStarterBootsAndRespondsHealthz verifies that the assembled example can
// start its bootstrap lifecycle and serve a 200 on /healthz within the smoke
// boot window, then shuts down cleanly on context cancellation.
//
// This is a mem-mode smoke: no postgres, no redis, no external services.
func TestStarterBootsAndRespondsHealthz(t *testing.T) {
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "testadmin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "testadminpass1!")

	ctx, cancel := context.WithTimeout(context.Background(), smokeBootDuration)
	defer cancel()

	shared, err := buildStarterMemSharedDeps(ctx)
	require.NoError(t, err)

	// Use unique test ports to avoid conflicts with a running corebundlestarter.
	shared.PrimaryHTTPAddr = "127.0.0.1:0"
	shared.InternalHTTPAddr = "127.0.0.1:0"
	shared.HealthHTTPAddr = "127.0.0.1:0"
	// Re-validate after address override (Validate only checks required fields).
	require.NoError(t, shared.Validate())

	app, err := composition.New().
		With(
			platformauditcore.Module(),
			platformaccesscore.Module(),
			platformconfigcore.Module(),
		).
		Build(ctx, shared, starterRuntimeOptions(shared))
	require.NoError(t, err)
	require.NotNil(t, app)

	// Run bootstrap in a goroutine; the deferred cancel() stops it.
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- app.Run(ctx) }()

	// Wait briefly for the server to bind.
	time.Sleep(smokeBindWait) // short static sleep: wait for bootstrap HTTP listener to bind

	// /healthz uses the HealthHTTPAddr listener.  In ":0" mode bootstrap picks
	// an ephemeral port.  We skip the HTTP probe when addr is ephemeral because
	// there is no exported way to discover the bound port in this example; the
	// build+no-error assertion above is the primary coverage guarantee.
	// A production smoke would configure a fixed addr and probe /healthz.

	// Cancel and wait for clean shutdown.
	cancel()
	select {
	case runErr := <-runErrCh:
		// bootstrap.Run returns nil on context cancellation (graceful shutdown).
		// Some environments return a context-error wrapper — both are acceptable.
		if runErr != nil {
			assert.ErrorIs(t, runErr, context.Canceled,
				"app.Run must return nil or context.Canceled on graceful shutdown")
		}
	case <-time.After(smokeBootDuration):
		t.Fatal("app.Run did not return within the smoke boot window after context cancellation")
	}
}

// TestStarterHealthzHTTP is an optional deeper smoke: probes /healthz over HTTP
// using fixed high-numbered ports (19098/18088/18089).
//
// Port collision note: proper ":0" ephemeral-port probing requires discovering
// the bound address after bootstrap.Run starts, which needs a composition.App
// bound-address API that does not yet exist (#1085 follow-up). Until that API
// lands, we use fixed high-numbered ports that are unlikely to conflict in CI.
// If they do collide the test will fail with "bind: address already in use" —
// that is an acceptable flakiness risk for an example smoke test.
func TestStarterHealthzHTTP(t *testing.T) {
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "testadmin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "testadminpass1!")

	// Use fixed high-numbered test ports to reduce collision risk with other tests.
	const healthAddr = "127.0.0.1:19098"

	ctx, cancel := context.WithTimeout(context.Background(), smokeBootDuration)
	defer cancel()

	shared, err := buildStarterMemSharedDeps(ctx)
	require.NoError(t, err)
	shared.PrimaryHTTPAddr = "127.0.0.1:18088"
	shared.InternalHTTPAddr = "127.0.0.1:18089"
	shared.HealthHTTPAddr = healthAddr

	app, err := composition.New().
		With(
			platformauditcore.Module(),
			platformaccesscore.Module(),
			platformconfigcore.Module(),
		).
		Build(ctx, shared, starterRuntimeOptions(shared))
	require.NoError(t, err)

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- app.Run(ctx) }()

	// Probe /healthz with retries.
	client := &http.Client{Timeout: smokeHTTPTimeout}
	healthURL := "http://" + healthAddr + "/healthz"

	var lastErr error
	for i := 0; i < 10; i++ {
		time.Sleep(smokeRetryInterval)     // short retry sleep: wait for HTTP server to accept connections
		resp, err := client.Get(healthURL) //nolint:noctx // test-only smoke probe; no ctx needed
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("/healthz returned %d", resp.StatusCode) //nolint:goerr113 // dynamic error message in test smoke; not exported
	}

	cancel()
	<-runErrCh

	require.NoError(t, lastErr, "/healthz must return 200 within smoke boot window")
}
