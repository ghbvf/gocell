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

func (fakeKeyHandle) Encrypt(_ context.Context, _, _ []byte) ([]byte, []byte, []byte, string, error) {
	return nil, nil, nil, "", errors.New("fakeKeyHandle.Encrypt must not be called in readiness tests")
}

func (fakeKeyHandle) Decrypt(_ context.Context, _, _, _, _ []byte) ([]byte, error) {
	return nil, errors.New("fakeKeyHandle.Decrypt must not be called in readiness tests")
}

var (
	_ kcrypto.KeyProvider             = (*fakeKeyProvider)(nil)
	_ kernellifecycle.ManagedResource = (*fakeKeyProvider)(nil)
	_ kcrypto.KeyHandle               = fakeKeyHandle{}
)

// buildBootstrapWithFakeKeyProvider is the test harness for A19. It mirrors
// buildBootstrapFromShared but injects ConfigCoreModule{KeyProviderOverride}
// so the readiness wiring can be exercised without GOCELL_CONFIGCORE_KEY_PROVIDER / Vault.
func buildBootstrapWithFakeKeyProvider(
	t *testing.T, shared *SharedDeps, kp kcrypto.KeyProvider,
	primaryLn net.Listener, extra ...bootstrap.Option,
) (*bootstrap.Bootstrap, error) {
	t.Helper()
	ctx := context.Background()

	cells, cellOpts, err := BuildApp(ctx, shared,
		ConfigCoreModule{KeyProviderOverride: kp},
		AccessCoreModule{ForceBootstrap: true, InitialAdminOpts: fastAdminBootstrapOpts()},
		AuditCoreModule{},
	)
	if err != nil {
		return nil, err
	}

	asm, err := buildAssembly(shared.PromStack, "corebundle", durabilityModeForTopology(shared.Topology), shared.Clock, cells...)
	if err != nil {
		return nil, err
	}

	consumerBase, err := buildConsumerBase(shared)
	if err != nil {
		return nil, err
	}

	metricsHandler := buildMetricsHandler(shared.MetricsToken, shared.PromStack.registry)

	adapterInfo := adapterInfoForSharedDeps(shared)
	opts := runtimeBaseOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
	opts = append(opts, cellOpts...)
	opts = append(opts, bootstrap.WithListener(
		cell.PrimaryListener,
		primaryLn.Addr().String(),
		[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
		bootstrap.WithListenerNet(primaryLn),
	))
	opts = append(opts, extra...)
	return newBootstrapFromOptions(opts), nil
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

	// /readyz?verbose must list the fake probe by name as unhealthy (proves the
	// aggregation step preserved the named checker, not just a boolean signal).
	// PR-A35: verbose is gated by PolicyVerboseToken on the readyz route group;
	// PR-A14b round-2 puts it on the dedicated HealthListener (healthAddr).
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

	// PR-A35 envelope: 503 /readyz responses carry the dependency breakdown
	// inside {"error": {"code":"ERR_SERVICE_UNAVAILABLE", "details": {...}}}
	// where "details" is the health-diagnostic object (not errcode array<{key,value}>).
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Status       string                    `json:"status"`
				Reason       string                    `json:"reason"`
				Dependencies map[string]map[string]any `json:"dependencies"`
			} `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(verboseResp.Body).Decode(&envelope))
	assert.Equal(t, string(errcode.ErrServiceUnavailable), envelope.Error.Code)
	assert.Equal(t, "service unavailable", envelope.Error.Message)
	assert.Equal(t, "unhealthy", envelope.Error.Details.Status)
	assert.Equal(t, "readiness_failed", envelope.Error.Details.Reason)
	probe, ok := envelope.Error.Details.Dependencies["fake_key_provider_ready"]
	require.True(t, ok, "fake_key_provider_ready must appear in /readyz?verbose error details")
	assert.Equal(t, "unhealthy", probe["status"],
		"fake_key_provider_ready must appear in /readyz?verbose as unhealthy")

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
