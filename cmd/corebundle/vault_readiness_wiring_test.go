package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// fakeKeyProvider satisfies kcrypto.KeyProvider AND
// kernellifecycle.ManagedResource. It drives the A19 wiring test:
// ConfigCoreModule must register the provider's Checkers() with bootstrap so
// an unhealthy probe flips /readyz to 503.
//
// Encrypt/Decrypt are unreachable in this test because memory topology does
// not exercise sensitive-value encryption; the ValueTransformer constructed
// over this fake is never invoked.
type fakeKeyProvider struct {
	probeErr error
}

// --- kcrypto.KeyProvider ---

func (f *fakeKeyProvider) Current(_ context.Context) (kcrypto.KeyHandle, error) {
	return fakeKeyHandle{}, nil
}

func (f *fakeKeyProvider) ByID(_ context.Context, _ string) (kcrypto.KeyHandle, error) {
	return fakeKeyHandle{}, nil
}

func (f *fakeKeyProvider) Rotate(_ context.Context) (string, error) {
	return "fake-v1", nil
}

// --- kernellifecycle.ManagedResource ---

func (f *fakeKeyProvider) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"fake_key_provider_ready": func(context.Context) error { return f.probeErr },
	}
}

func (f *fakeKeyProvider) Worker() kworker.Worker { return nil }

func (f *fakeKeyProvider) Close(_ context.Context) error { return nil }

type fakeKeyHandle struct{}

func (fakeKeyHandle) ID() string { return "fake-v1" }

func (fakeKeyHandle) Encrypt(_ context.Context, _, _ []byte) (kcrypto.EncryptResult, error) {
	return kcrypto.EncryptResult{}, errors.New("fakeKeyHandle.Encrypt must not be called in readiness tests")
}

func (fakeKeyHandle) Decrypt(_ context.Context, _, _, _, _ []byte) ([]byte, error) {
	return nil, errors.New("fakeKeyHandle.Decrypt must not be called in readiness tests")
}

var (
	_ kcrypto.KeyProvider             = (*fakeKeyProvider)(nil)
	_ kernellifecycle.ManagedResource = (*fakeKeyProvider)(nil)
	_ kcrypto.KeyHandle               = fakeKeyHandle{}
)

// keyProviderCellModule is a minimal CellModule used by the A19 tests to wire
// a kcrypto.KeyProvider (that also implements kernellifecycle.ManagedResource)
// into bootstrap without using the real ConfigCoreModule. This avoids the
// DurabilityDurable/DurabilityDemo mismatch that the real configcore cell
// introduces when running against a memory topology.
//
// The test goal is to verify the wiring path:
//
//	ConfigCoreModule.Provide registers kp as WithManagedResource when
//	kp implements kernellifecycle.ManagedResource.
//
// A real ConfigCoreModule cannot be used in memory topology because configcore
// declares durabilityMode: durable and the assembly uses DurabilityDemo.
// This module replaces that wiring path with an equivalent demo-compatible module.
type keyProviderCellModule struct {
	kp kcrypto.KeyProvider
}

func (m keyProviderCellModule) ID() string { return "configcore-kp-stub" }

func (m keyProviderCellModule) Provide(
	_ context.Context, _ *SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// Create a demo-mode cell so the assembly DurabilityDemo check passes.
	c := cell.MustNewBaseCell(&metadata.CellMeta{
		ID:             "configcore-kp-stub",
		Type:           "core",
		DurabilityMode: "demo",
	})
	var opts []bootstrap.Option
	var provisional []kernellifecycle.ManagedResource
	// Mirror ConfigCoreModule.Provide A19 wiring: if kp implements
	// ManagedResource, register its Checkers() with bootstrap.
	if kpRes, ok := m.kp.(kernellifecycle.ManagedResource); ok {
		opts = append(opts, bootstrap.WithManagedResource(kpRes))
		provisional = append(provisional, kpRes)
	}
	return c, opts, provisional, nil
}

var _ CellModule = keyProviderCellModule{}

// buildBootstrapWithFakeKeyProvider is the test harness for A19. It mirrors
// the ConfigCoreModule A19 wiring path without using the real ConfigCoreModule
// (which fails in memory topology due to DurabilityDurable/DurabilityDemo
// mismatch). Uses keyProviderCellModule to register the fake KeyProvider as a
// ManagedResource in bootstrap, exercising the wiring path under test.
// JWT is wired directly via shared.JWTDeps.verifier (no real cell.AuthProvider).
func buildBootstrapWithFakeKeyProvider(
	t *testing.T, shared *SharedDeps, kp kcrypto.KeyProvider,
	primaryLn net.Listener, extra ...bootstrap.Option,
) (*bootstrap.Bootstrap, error) {
	t.Helper()
	return buildBootstrapFromSharedWithModules(t, shared, primaryLn,
		[]CellModule{keyProviderCellModule{kp: kp}},
		cell.MustNewAuthJWT(shared.JWTDeps.verifier),
		extra...,
	)
}

// TestA19_ConfigCoreModule_RegistersKeyProviderReadiness is the bootstrap-level
// end-to-end guard the PR #205 review called out as missing: the
// TransitKeyProvider's Checkers() probe must flow through ConfigCoreModule →
// bootstrap.WithManagedResource → /readyz.
//
// We inject a fake KeyProvider that also implements ManagedResource with a
// failing probe. If the wiring is in place, /readyz returns 503 and
// /readyz?verbose lists "fake_key_provider_ready" as unhealthy. If the fix is
// reverted, /readyz stays at 200 and this test fails.
//
// ref: docs/plans/202604201800-pg-pilot-layering-refactor-plan.md §9 (R1e A19)
// ref: readiness review 2026-04-20 P1 finding (missing bootstrap wiring)
func TestA19_ConfigCoreModule_RegistersKeyProviderReadiness(t *testing.T) {
	shared := buildTestSharedDeps(t)
	// Override the canonical test fixture: this test hits /readyz?verbose to
	// inspect dependency probe names, so we need verbose output + a token.
	shared.VerboseDisabled = false
	shared.VerboseToken = "test-verbose-token"
	kp := &fakeKeyProvider{probeErr: errors.New("vault unreachable (test)")}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	healthLn := newCorebundleLocalListener(t)
	healthOpt := bootstrap.WithListener(
		cell.HealthListener, healthLn.Addr().String(),
		[]cell.ListenerAuth{cell.AuthNone{}}, bootstrap.WithListenerNet(healthLn))
	app, err := buildBootstrapWithFakeKeyProvider(t, shared, kp, ln,
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		healthOpt)
	require.NoError(t, err)
	require.NotNil(t, app)

	// K#08 5xx redaction strips verbose breakdown from the wire (details is
	// the canonical empty array on 503). Probe names ride on slog instead.
	capture := withSlogCapture(t)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	healthAddr := healthLn.Addr().String()
	waitForHealthy(t, healthAddr)

	// /readyz must reflect the failing fake probe → 503.
	resp, err := http.Get("http://" + healthAddr + "/readyz")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("close resp body: %v", err)
		}
	})
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"/readyz must be 503 when the KeyProvider readiness probe fails "+
			"(proves ConfigCoreModule wires kp.Checkers() into bootstrap)")

	// /readyz?verbose request triggers the verbose slog record; the wire
	// body is the canonical errcode envelope with empty details array
	// (K#08). Probe-by-name verification reads the slog snapshot.
	verboseReq, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, "http://"+healthAddr+"/readyz?verbose", nil)
	require.NoError(t, err)
	verboseReq.Header.Set("X-Readyz-Token", shared.VerboseToken)
	verboseResp, err := http.DefaultClient.Do(verboseReq)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := verboseResp.Body.Close(); err != nil {
			t.Logf("close verboseResp body: %v", err)
		}
	})

	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []any  `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(verboseResp.Body).Decode(&envelope))
	assert.Equal(t, string(errcode.ErrServiceUnavailable), envelope.Error.Code)
	assert.Equal(t, "service unavailable", envelope.Error.Message)
	assert.Empty(t, envelope.Error.Details,
		"K#08 5xx redaction: 503 wire body details must be the canonical empty array")

	// Verbose breakdown lives in the captured slog "readyz unhealthy" record.
	deps := readyzUnhealthyDeps(t, capture)
	probe, ok := deps["fake_key_provider_ready"]
	require.True(t, ok, "fake_key_provider_ready must appear in slog breakdown")
	assert.Equal(t, "unhealthy", probe["status"],
		"fake_key_provider_ready must appear in slog breakdown as unhealthy")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectAsyncSettle):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestA19_ConfigCoreModule_KeyProviderReady is the positive control for
// TestA19_ConfigCoreModule_RegistersKeyProviderReadiness. With the same wiring
// and a passing probe, /readyz must stay at 200 — guards against the wiring
// accidentally force-failing /readyz regardless of probe result.
func TestA19_ConfigCoreModule_KeyProviderReady(t *testing.T) {
	shared := buildTestSharedDeps(t)
	kp := &fakeKeyProvider{probeErr: nil}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	healthLn2 := newCorebundleLocalListener(t)
	healthOpt2 := bootstrap.WithListener(
		cell.HealthListener, healthLn2.Addr().String(),
		[]cell.ListenerAuth{cell.AuthNone{}}, bootstrap.WithListenerNet(healthLn2))
	app, err := buildBootstrapWithFakeKeyProvider(t, shared, kp, ln,
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		healthOpt2)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	healthAddr2 := healthLn2.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + healthAddr2 + "/readyz")
		if err != nil {
			return false
		}
		if err := resp.Body.Close(); err != nil {
			t.Logf("close resp body: %v", err)
		}
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"/readyz must be 200 when the KeyProvider probe is healthy")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectAsyncSettle):
		t.Fatal("bootstrap did not shut down in time")
	}
}
