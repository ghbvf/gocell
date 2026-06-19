//go:build integration

package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/auth/authtest"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFrameworkServing_Smoke_DevicestateReturns401WhenNoToken boots the full
// corebundle assembly (including framework serving) and asserts that
// GET /api/v1/devicestate/{id} returns 401 Unauthorized without a token.
//
// Key assertion rationale: 401 ≠ 404.
//   - If WithFrameworkHTTPServing was not called (or the route was never
//     mounted), the router has no handler for the path → 404 Not Found.
//   - 401 Unauthorized proves that:
//     (1) the framework-owned RouteGroup was mounted successfully (phase5),
//     (2) the device:read PDP gate on the PrimaryListener JWT chain fired
//     before the handler could return a 2xx or domain error.
//
// This test provides end-to-end coverage of:
//   - phase0 validateFrameworkServing reconcile (assembly declares
//     "http.devicestate.v1" and the option is wired → no startup error),
//   - phase5 route mount onto the PrimaryListener mux,
//   - the JWT-chain auth middleware rejecting an unauthenticated request.
//
// No token is minted: the 401 path is sufficient to prove mounted+gated;
// exercising the authorized path is a separate functional concern.
func TestFrameworkServing_Smoke_DevicestateReturns401WhenNoToken(t *testing.T) {
	shared := buildTestSharedDeps(t)

	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	healthLn := newCorebundleLocalListener(t)
	internalLn := newCorebundleLocalListener(t)

	app, err := buildBootstrapFromShared(
		t, shared, primaryLn,
		withCorebundleTestInternalListener(t, internalLn),
		bootstrap.WithListener(
			cell.HealthListener,
			healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			bootstrap.WithListenerNet(healthLn),
		),
	)
	require.NoError(t, err, "buildBootstrapFromShared must succeed: framework serving is correctly wired")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(testtime.SelectShutdown):
			t.Error("bootstrap did not shut down in time")
		}
	})

	// Wait until the health listener is ready before probing the primary listener.
	waitForHealthy(t, healthLn.Addr().String())

	base := "http://" + primaryLn.Addr().String()

	// GET /api/v1/devicestate/{id} without a Bearer token.
	// Expected: 401 Unauthorized (route is mounted and the JWT auth chain fires).
	// If framework serving was never mounted: 404 Not Found would be returned
	// instead, and this assertion would catch the regression. The id segment is
	// required (path param), so it is supplied to reach the mounted route.
	resp, err := http.Get(base + "/api/v1/devicestate/dev-1")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"GET /api/v1/devicestate/{id} without a token must return 401 Unauthorized. "+
			"401 ≠ 404: a 404 would indicate the framework RouteGroup was never mounted "+
			"(WithFrameworkHTTPServing missing or phase5 mount failed). "+
			"401 proves the route is mounted and the device:read PDP gate fired. "+
			"Covers #2348 review F2: full bootstrap must successfully wire framework serving.")
}

// TestFrameworkServing_Smoke_DeviceIdentityRoutesMounted boots the full
// corebundle assembly (including device-identity EST serving wired by PR-8b
// #1904) and asserts two smoke sub-cases on the PrimaryListener:
//
// (a) GET /api/v1/deviceidentity/cacerts → 200 + body contains "trustBundle"
//
//	The cacerts route is public (RFC 7030 §4.1 unauthenticated CA trust
//	distribution). A 200 with the expected JSON field proves the route is
//	mounted AND the softca signer produced a trust bundle. A 404 would
//	indicate WithFrameworkHTTPServing was not wired or the route was never
//	registered.
//
// (b) POST /api/v1/deviceidentity/enroll (no Authorization header) → 401
//
//	The enroll route is public at the listener level (auth.public:true skips
//	the JWT gate) but requires an application-layer enrollment-credential bearer
//	token. A missing header triggers a typed 401 response from the handler's
//	EnrollmentCredentialVerifier middleware. A 404 would indicate the route was
//	never mounted; a 200 would indicate the application-layer auth gate is
//	broken.
//
// Both sub-cases use the same bootstrapped app instance from
// buildBootstrapFromShared, which already wires enroll/cacerts (PrimaryListener)
// and renew (DeviceMTLSListener) as part of deviceIdentityServingOptions.
func TestFrameworkServing_Smoke_DeviceIdentityRoutesMounted(t *testing.T) {
	shared := buildTestSharedDeps(t)

	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	healthLn := newCorebundleLocalListener(t)
	internalLn := newCorebundleLocalListener(t)

	app, err := buildBootstrapFromShared(
		t, shared, primaryLn,
		withCorebundleTestInternalListener(t, internalLn),
		bootstrap.WithListener(
			cell.HealthListener,
			healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			bootstrap.WithListenerNet(healthLn),
		),
	)
	require.NoError(t, err, "buildBootstrapFromShared must succeed: device-identity serving is correctly wired")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(testtime.SelectShutdown):
			t.Error("bootstrap did not shut down in time")
		}
	})

	waitForHealthy(t, healthLn.Addr().String())

	base := "http://" + primaryLn.Addr().String()

	// (a) GET /api/v1/deviceidentity/cacerts — public, no token required.
	// Expected: 200 OK with a JSON body containing a "trustBundle" field.
	// A 404 would indicate the cacerts route was never mounted via
	// WithFrameworkHTTPServing.
	cacertsResp, err := http.Get(base + "/api/v1/deviceidentity/cacerts")
	require.NoError(t, err)
	defer cacertsResp.Body.Close()

	assert.Equal(t, http.StatusOK, cacertsResp.StatusCode,
		"GET /api/v1/deviceidentity/cacerts without a token must return 200 OK "+
			"(public RFC 7030 §4.1 CA trust distribution). "+
			"404 would indicate the cacerts framework route was never mounted.")

	assert.Contains(t, cacertsResp.Header.Get("Content-Type"), "application/json",
		"cacerts response must be JSON")

	// Read up to 4 KiB — sufficient to find the trustBundle field.
	buf := make([]byte, 4096)
	n, _ := cacertsResp.Body.Read(buf)
	body := string(buf[:n])
	assert.Contains(t, body, "trustBundle",
		"cacerts response body must contain 'trustBundle' field (RFC 7030 §4.1 CA material). "+
			"Absence means the signer did not produce a trust bundle or the response schema changed.")

	// (b) POST /api/v1/deviceidentity/enroll — no Authorization header.
	// Expected: 401 Unauthorized (application-layer enrollment-credential gate fires).
	// A 404 would indicate the enroll route was never mounted.
	// A 200 would indicate the application-layer auth gate is broken.
	enrollResp, err := http.Post(base+"/api/v1/deviceidentity/enroll", "application/json",
		strings.NewReader(`{"csr":"dGVzdA==","deviceId":"11111111-1111-1111-1111-111111111111"}`))
	require.NoError(t, err)
	defer enrollResp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, enrollResp.StatusCode,
		"POST /api/v1/deviceidentity/enroll without Authorization header must return 401. "+
			"401 proves: (1) the enroll route is mounted on PrimaryListener, "+
			"(2) the EnrollmentCredentialVerifier middleware fired and rejected the missing bearer token. "+
			"404 would mean the route was never registered; 200 would mean auth gate is broken.")
}

// TestFrameworkServing_Regression_OmitOptionCausesStartupFailFast verifies
// that omitting bootstrap.WithFrameworkHTTPServing while the assembly declares
// framework contracts causes a Run-time phase0 error — not a silent 404.
//
// Scenario (#2348 review F1):
//   - buildAssembly(..., generatedFrameworkServedContracts(), cells...) declares
//     "http.devicestate.v1" in assembly.Config.FrameworkContracts (non-empty
//     must-serve expectation set).
//   - The runtimeOptsFunc intentionally does NOT append
//     bootstrap.WithFrameworkHTTPServing(...), so b.frameworkServingRoutes is
//     empty while b.frameworkServedExpected() is non-empty.
//   - bootstrap.Run() phase0 (validateFrameworkServing) reconciles expected vs
//     provided and must return an error containing "frameworkContracts" (stable
//     sub-string from framework_serving.go validateFrameworkServing).
//
// Note: validateFrameworkServing is a phase0 check inside Run(), not Build().
// composition.Builder.Build() succeeds (option wiring is valid at build time);
// the reconcile happens in Run() before any HTTP server is started. This
// matches the pattern of checkOrphanGRPCServices (#2204) and SPIRE catalog.Load.
//
// This test closes the regression gap: before #2037 a missing option produced
// a silent dead 404; now it is a startup fail-fast.
func TestFrameworkServing_Regression_OmitOptionCausesStartupFailFast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shared := buildTestSharedDeps(t)
	_, locals := buildTestSharedDepsAndLocals(t)

	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = primaryLn.Close() })

	healthLn := newCorebundleLocalListener(t)

	// runtimeOptsFunc mirrors buildBootstrapFromShared but intentionally omits
	// bootstrap.WithFrameworkHTTPServing to trigger the phase0 fail-fast.
	// The gRPC listener is retained (corebundleTestGRPCListenerOption) so
	// checkOrphanGRPCServices does not fire before validateFrameworkServing.
	runtimeOptsFunc := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		asm, err := buildAssembly(
			locals,
			"corebundle-test-no-framework-serving",
			durabilityModeForTopology(shared.Topology),
			shared.Clock,
			generatedFrameworkServedContracts(), // assembly declares "http.devicestate.v1"
			cells...,
		)
		if err != nil {
			return nil, err
		}

		consumerBase, err := buildConsumerBase(shared)
		if err != nil {
			return nil, err
		}

		adapterInfo := adapterInfoForSharedDeps(shared, locals)
		metricsHandler := buildMetricsHandler(shared.MetricsToken, locals.registry)
		opts := runtimeBaseOptions(shared, locals, asm, consumerBase, metricsHandler, adapterInfo)

		opts = append(
			opts,
			bootstrap.WithListener(
				cell.PrimaryListener,
				primaryLn.Addr().String(),
				[]kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
				bootstrap.WithListenerNet(primaryLn),
			),
			// gRPC listener is mandatory: accesscore registers
			// grpc.auth.session.verify.v1 unconditionally (PR-11 #1154); without
			// a gRPC listener checkOrphanGRPCServices fires before
			// validateFrameworkServing and the error reason would differ from what
			// this test intends to verify.
			corebundleTestGRPCListenerOption(t, cells, asm.CellIDs()),
			bootstrap.WithListener(
				cell.HealthListener,
				healthLn.Addr().String(),
				[]kauth.ListenerAuth{kauth.AuthNone{}},
				bootstrap.WithListenerNet(healthLn),
			),
			// NOTE: WithFrameworkHTTPServing is intentionally absent here.
			// The assembly above declares "http.devicestate.v1" via
			// generatedFrameworkServedContracts(), so b.frameworkServingRoutes
			// will be empty while b.frameworkServedExpected() is non-empty.
			// phase0 validateFrameworkServing (inside Run()) must detect the
			// mismatch and return an error — this is the regression being guarded.
		)
		return opts, nil
	}

	mods := generatedCellModules()
	// Build succeeds: option wiring is structurally valid. The mismatch is only
	// detectable at phase0 (Run time), after Init has populated assembly contracts.
	app, buildErr := composition.New(corebundleCellIDs()...).With(mods...).Build(ctx, shared, runtimeOptsFunc)
	require.NoError(t, buildErr, "composition.Build must succeed; the mismatch is caught at phase0 (Run), not Build")
	require.NotNil(t, app, "app must be non-nil after Build")

	// Run() must fail fast at phase0 (validateFrameworkServing) before any
	// HTTP listener is started. The error must surface within a short timeout —
	// phase0 is synchronous and returns immediately on the first violation.
	runErr := app.Run(ctx)

	require.Error(t, runErr,
		"Run() must fail when the assembly declares framework contracts "+
			"but WithFrameworkHTTPServing is not called; "+
			"phase0 validateFrameworkServing must catch the mismatch and fail-fast "+
			"(#2348 review F1 regression guard)")

	// Assert on a stable sub-string from framework_serving.go validateFrameworkServing.
	// "missing_contracts" is the InternalAttr key emitted when the assembly's
	// frameworkContracts expectation set has entries with no wired RouteGroup.
	// It is stable, unique to this error path, and visible in the errcode Error()
	// output (internal attrs are included in the Go error string, not stripped).
	assert.Contains(t, runErr.Error(), "missing_contracts",
		"the startup error must mention 'missing_contracts' (the InternalAttr key "+
			"from validateFrameworkServing) so operators can identify the missing "+
			"WithFrameworkHTTPServing option as the cause; full error: %s", runErr.Error())
}
