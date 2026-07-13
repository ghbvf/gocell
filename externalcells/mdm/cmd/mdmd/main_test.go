package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/certlifecycle"

	"github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status"
	statusmem "github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status/mem"
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

// ── Status endpoint integration tests ─────────────────────────────────────────

// appTestEnv holds the JWT issuer, address, and status repository for status
// integration tests. buildAppFromShared is called with the SAME shared used here so
// JWT tokens issued by env.jwtIssuer are verifiable by the running app.
// statusRepo is the live repository seeded by the test before requests are issued.
type appTestEnv struct {
	jwtIssuer  *auth.JWTIssuer
	primary    string
	statusRepo *statusmem.Repository
}

// startDemoAppWithIssuer starts the demo app and returns the JWT issuer that the
// running server will accept tokens from (same key pair — no separate key exchange
// needed). The test may call cancel() to stop early; t.Cleanup handles shutdown.
//
// The returned appTestEnv.statusRepo is the live repository: tests can seed records
// into it before (or after) issuing HTTP requests to the running app.
func startDemoAppWithIssuer(t *testing.T) appTestEnv {
	t.Helper()
	t.Setenv(demoOptInEnv, "1")

	primaryLn := mustLoopbackListener(t)
	internalLn := mustLoopbackListener(t)
	healthLn := mustLoopbackListener(t)

	addrs := listenerAddrs{
		primary:  primaryLn.Addr().String(),
		internal: internalLn.Addr().String(),
		health:   healthLn.Addr().String(),
	}

	// Build shared deps ONCE in the test process; buildAppFromShared will use the
	// SAME shared (same JWT key pair), so tokens issued via shared.JWTIssuer are
	// accepted by the running app's verifier.
	shared, err := buildMemSharedDeps(addrs)
	if err != nil {
		t.Fatalf("buildMemSharedDeps: %v", err)
	}

	// Construct the repository here (composition root owns topology-dependent resources)
	// so the test can seed records before requests are issued.
	repo := statusmem.New(shared.Clock)

	ctx, cancel := context.WithCancel(context.Background())

	runErr := make(chan error, 1)
	go func() {
		app, appErr := buildAppFromShared(ctx, shared, repo, prebuiltListeners{
			primary:  primaryLn,
			internal: internalLn,
			health:   healthLn,
		})
		if appErr != nil {
			runErr <- appErr
			return
		}
		runErr <- app.Run(ctx)
	}()

	deadline := time.Now().Add(startupPollTimeout)
	if !pollGreen("http://"+addrs.health+"/healthz", deadline) {
		cancel()
		t.Fatal("app never became healthy within timeout")
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-runErr:
		case <-time.After(shutdownTimeout):
			t.Errorf("app did not shut down within %s", shutdownTimeout)
		}
	})

	return appTestEnv{jwtIssuer: shared.JWTIssuer, primary: addrs.primary, statusRepo: repo}
}

// issueAppJWT signs a JWT with the given subject and roles using the app's JWT issuer.
func issueAppJWT(t *testing.T, env appTestEnv, subject string, roles []string) string {
	t.Helper()
	tok, err := env.jwtIssuer.Issue(auth.TokenIntentAccess, subject, auth.IssueOptions{
		Roles: roles,
	})
	if err != nil {
		t.Fatalf("JWTIssuer.Issue(%q, roles=%v): %v", subject, roles, err)
	}
	return tok
}

// doStatusGET executes GET /api/v1/deviceidentity/status/<deviceID>
// against the primary listener, optionally with a Bearer token (path-param, #2426 F1).
func doStatusGET(t *testing.T, primaryAddr, deviceID, bearerToken string) *http.Response {
	t.Helper()
	url := fmt.Sprintf("http://%s/api/v1/deviceidentity/status/%s", primaryAddr, deviceID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// TestRun_StatusEndpoint_401_Unauthenticated: no Authorization header → 401.
func TestRun_StatusEndpoint_401_Unauthenticated(t *testing.T) {
	env := startDemoAppWithIssuer(t)

	resp := doStatusGET(t, env.primary, "dev-unknown", "")
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 401, got %d; body=%s", resp.StatusCode, body)
	}
}

// TestRun_StatusEndpoint_403_NoRole: valid JWT but no mdm-admin/mdm-operator role → 403.
// The JWT verifier accepts the token (correct key pair + iss/aud), but the enrollAuthorizer
// PDP denies device:read because no recognized role is present.
func TestRun_StatusEndpoint_403_NoRole(t *testing.T) {
	env := startDemoAppWithIssuer(t)

	tok := issueAppJWT(t, env, "user-no-role", []string{})
	resp := doStatusGET(t, env.primary, "dev-unknown", tok)
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 403 (no role), got %d; body=%s", resp.StatusCode, body)
	}
}

// TestRun_StatusEndpoint_404_UnknownDevice: valid JWT with mdm-admin role + empty repo → 404.
// PDP allows the request (admin role → device:read allow), service looks up the
// in-memory repo (starts empty) and returns 404.
func TestRun_StatusEndpoint_404_UnknownDevice(t *testing.T) {
	env := startDemoAppWithIssuer(t)

	tok := issueAppJWT(t, env, "admin-1", []string{"mdm-admin"})
	resp := doStatusGET(t, env.primary, "non-existent-device", tok)
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 404 (unknown device), got %d; body=%s", resp.StatusCode, body)
	}
}

// TestRun_StatusEndpoint_200_FoundDevice: valid JWT with mdm-admin role + seeded
// CertRecord → 200 with response body containing "data" and correct deviceId field.
// This is the happy-path integration test: proves the full request pipeline from JWT
// auth through PDP to service to JSON response.
func TestRun_StatusEndpoint_200_FoundDevice(t *testing.T) {
	env := startDemoAppWithIssuer(t)

	// Seed the repository with a known CertRecord before issuing the request.
	clk := clock.Real()
	now := clk.Now()
	env.statusRepo.Put(status.CertRecord{
		DeviceID:  "dev-seed-1",
		Issuer:    "CN=MDM-TestCA",
		Serial:    "BEEF0001",
		State:     certlifecycle.StateActive(),
		NotBefore: now.Add(-24 * time.Hour),
		NotAfter:  now.Add(364 * 24 * time.Hour),
		Epoch:     1,
	})

	tok := issueAppJWT(t, env, "admin-test", []string{"mdm-admin"})
	resp := doStatusGET(t, env.primary, "dev-seed-1", tok)
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d; body=%s", resp.StatusCode, body)
	}

	// Parse enough of the response to assert the core fields are present.
	var envelope struct {
		Data struct {
			DeviceID string `json:"deviceId"`
			Status   string `json:"status"`
		} `json:"data"`
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal 200 body: %v; body=%s", err, body)
	}
	if envelope.Data.DeviceID != "dev-seed-1" {
		t.Errorf("data.deviceId: got %q, want %q", envelope.Data.DeviceID, "dev-seed-1")
	}
	if envelope.Data.Status != "active" {
		t.Errorf("data.status: got %q, want %q", envelope.Data.Status, "active")
	}
}

// TestRun_StatusEndpoint_401_InvalidToken: a malformed/garbage Bearer token → 401.
// This verifies the primary listener JWT verifier enforces signature validation, not
// just presence of a token. Distinct from the unauthenticated (no header) case.
func TestRun_StatusEndpoint_401_InvalidToken(t *testing.T) {
	env := startDemoAppWithIssuer(t)

	resp := doStatusGET(t, env.primary, "dev-any", "not-a-valid-jwt-token")
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 401 (invalid token), got %d; body=%s", resp.StatusCode, body)
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
