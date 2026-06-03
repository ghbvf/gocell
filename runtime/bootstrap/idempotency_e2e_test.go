package bootstrap

// idempotency_e2e_test.go — bootstrap-level end-to-end tests for HTTP
// idempotency replay (F12).
//
// Test level choice: bootstrap (not router/idempotency level) because the
// wiring under test is WithIdempotencyStore → router.WithIdempotency → the
// middleware installed AFTER auth. Standing up a real bootstrap listener also
// exercises the full auth chain (JWT → PrincipalUser) that the middleware
// depends on. The router-level tests (TestWithIdempotency_MiddlewareRunsAfterAuth,
// TestIdempotencyExempt_ExemptRouteNotRecorded) already cover the middleware
// internals; these tests cover the bootstrap wiring layer.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	kauthtest "github.com/ghbvf/gocell/kernel/auth/authtest"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// idemTestVerifier is a minimal IntentTokenVerifier for idempotency e2e tests.
// It always returns a PrincipalUser with Subject "user-1" to satisfy the
// idempotency middleware prerequisite (PrincipalUser + non-empty Subject).
type idemTestVerifier struct {
	claims kauth.Claims
}

func (v *idemTestVerifier) Verify(_ context.Context, _ string) (kauth.Claims, error) {
	return v.claims, nil
}

func (v *idemTestVerifier) VerifyIntent(_ context.Context, _ string, _ kauth.TokenIntent) (kauth.Claims, error) {
	return v.claims, nil
}

// idemCountingCell registers a POST /api/v1/orders route whose handler
// increments a counter on each invocation. Used to assert that replay
// suppresses handler execution.
type idemCountingCell struct {
	*cell.BaseCell
	handlerCalls *atomic.Int32
	// exemptCalls tracks calls to the IdempotencyExempt route specifically.
	exemptCalls *atomic.Int32
}

func newIdemCountingCell(id string) *idemCountingCell {
	return &idemCountingCell{
		BaseCell:     cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
		handlerCalls: new(atomic.Int32),
		exemptCalls:  new(atomic.Int32),
	}
}

func (c *idemCountingCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	reg.RouteGroup(cell.RouteGroup{
		Listener: cell.PrimaryListener,
		Prefix:   "",
		Register: func(mux cell.RouteMux) error {
			// Normal POST — idempotency-tracked.
			if err := auth.Mount(mux, auth.Route{
				Contract: testHTTPContract(http.MethodPost, "/api/v1/orders"),
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					c.handlerCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"order":"1"}`))
				}),
			}); err != nil {
				return err
			}
			// IdempotencyExempt POST — never recorded, always re-executes.
			return auth.Mount(mux, auth.Route{
				Contract: testHTTPContract(http.MethodPost, "/api/v1/orders/bulk"),
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					c.exemptCalls.Add(1)
					w.WriteHeader(http.StatusCreated)
				}),
				IdempotencyExempt: true,
			})
		},
	})
	return nil
}

// bootstrapIdemApp builds a minimal bootstrap app with:
//   - a primary listener (JWT auth, verifier returns Subject:"user-1")
//   - a MemStore-backed idempotency store
//   - the idemCountingCell
//
// Returns the primary listener address, the cell, and a cancel func.
// The caller is responsible for calling cancel to stop the app.
func bootstrapIdemApp(t *testing.T) (primaryAddr string, counting *idemCountingCell, cancel context.CancelFunc) {
	t.Helper()

	clk := clock.Real()
	memStore := idemhttp.NewMemStore(clk)
	verifier := &idemTestVerifier{claims: kauth.Claims{Subject: "user-1"}}

	asm := assembly.New(clk, assembly.Config{ID: "test-idem-e2e", DurabilityMode: outbox.DurabilityDemo})
	counting = newIdemCountingCell("idem-cell")
	require.NoError(t, asm.Register(counting))

	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = primaryLn.Close() })

	healthLn := newLocalListener(t)

	b := New(
		clk,
		WithAssembly(asm),
		WithIdempotencyStore(memStore),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(),
			[]kauth.ListenerAuth{kauthtest.MustAuthJWT(verifier)},
			WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, "127.0.0.1:0",
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			WithListenerNet(newLocalListener(t))),
		WithListener(cell.HealthListener, healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			WithListenerNet(healthLn)),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancelFn := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	waitForHealthy(t, healthLn.Addr().String())
	t.Cleanup(func() {
		cancelFn()
		select {
		case <-done:
		case <-time.After(testtime.D5s):
			t.Error("bootstrap did not shut down in time")
		}
	})

	return primaryLn.Addr().String(), counting, cancelFn
}

// TestBootstrap_IdempotencyReplay_SecondRequestReplayed is the main F12 test:
// a POST with Idempotency-Key executes the handler on the first call and
// replays the stored response on the second call — proving the middleware is
// correctly wired via WithIdempotencyStore through the bootstrap stack.
//
// HARD verification: a second route declared IdempotencyExempt:true is never
// recorded — the handler always runs and Idempotency-Replayed is never set.
func TestBootstrap_IdempotencyReplay_SecondRequestReplayed(t *testing.T) {
	addr, counting, _ := bootstrapIdemApp(t)

	makePost := func(path, key string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			fmt.Sprintf("http://%s%s", addr, path),
			bytes.NewBufferString(`{}`))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer valid-token")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		return resp
	}

	// ── Normal route (idempotency-tracked) ──────────────────────────────────

	// First request: handler must execute.
	resp1 := makePost("/api/v1/orders", "order-key-001")
	defer closeBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode,
		"first request must return 201")
	assert.Equal(t, int32(1), counting.handlerCalls.Load(),
		"handler must execute on first request")
	assert.Empty(t, resp1.Header.Get("Idempotency-Replayed"),
		"Idempotency-Replayed must NOT be set on first request")

	// Second request with same key: handler must NOT execute; response is replayed.
	resp2 := makePost("/api/v1/orders", "order-key-001")
	defer closeBody(t, resp2)
	require.Equal(t, http.StatusCreated, resp2.StatusCode,
		"replayed request must return the same status")
	assert.Equal(t, int32(1), counting.handlerCalls.Load(),
		"handler must NOT execute again for replayed request")
	assert.Equal(t, "true", resp2.Header.Get("Idempotency-Replayed"),
		"replayed response must carry Idempotency-Replayed: true")

	// ── Exempt route (IdempotencyExempt:true) ────────────────────────────────

	// First call to the exempt route.
	resp3 := makePost("/api/v1/orders/bulk", "bulk-key-001")
	defer closeBody(t, resp3)
	require.Equal(t, http.StatusCreated, resp3.StatusCode)
	assert.Equal(t, int32(1), counting.exemptCalls.Load(),
		"exempt handler must execute on first request")
	assert.Empty(t, resp3.Header.Get("Idempotency-Replayed"),
		"Idempotency-Replayed must not be set for exempt route")

	// Second call to the exempt route with the same Idempotency-Key:
	// handler must execute again (never recorded in store).
	resp4 := makePost("/api/v1/orders/bulk", "bulk-key-001")
	defer closeBody(t, resp4)
	require.Equal(t, http.StatusCreated, resp4.StatusCode)
	assert.Equal(t, int32(2), counting.exemptCalls.Load(),
		"exempt handler must execute on every request — no replay")
	assert.Empty(t, resp4.Header.Get("Idempotency-Replayed"),
		"Idempotency-Replayed must never be set for exempt route")
}

// TestBootstrap_WithIdempotencyStore_InstallsMiddleware verifies that when
// WithIdempotencyStore is NOT called the header Idempotency-Replayed is never
// set (no middleware = passthrough), confirming that the option correctly gates
// the middleware installation.
func TestBootstrap_WithIdempotencyStore_InstallsMiddleware(t *testing.T) {
	// Build a bootstrap app WITHOUT the idempotency store.
	clk := clock.Real()
	verifier := &idemTestVerifier{claims: kauth.Claims{Subject: "user-1"}}

	asm := assembly.New(clk, assembly.Config{ID: "test-idem-no-store", DurabilityMode: outbox.DurabilityDemo})
	counting := newIdemCountingCell("no-store-cell")
	require.NoError(t, asm.Register(counting))

	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = primaryLn.Close() })
	healthLn := newLocalListener(t)

	b := New(
		clk,
		WithAssembly(asm),
		// Intentionally NO WithIdempotencyStore.
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(),
			[]kauth.ListenerAuth{kauthtest.MustAuthJWT(verifier)},
			WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, "127.0.0.1:0",
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			WithListenerNet(newLocalListener(t))),
		WithListener(cell.HealthListener, healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			WithListenerNet(healthLn)),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	waitForHealthy(t, healthLn.Addr().String())
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testtime.D5s):
			t.Error("bootstrap did not shut down in time")
		}
	}()

	makePost := func(key string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			fmt.Sprintf("http://%s/api/v1/orders", primaryLn.Addr().String()),
			bytes.NewBufferString(`{}`))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer valid-token")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		return resp
	}

	// First request.
	resp1 := makePost("order-k-001")
	defer closeBody(t, resp1)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	assert.Equal(t, int32(1), counting.handlerCalls.Load())

	// Second request — without a store, no replay occurs, handler executes again.
	resp2 := makePost("order-k-001")
	defer closeBody(t, resp2)
	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	assert.Equal(t, int32(2), counting.handlerCalls.Load(),
		"without an idempotency store, handler must execute on every request")
	assert.Empty(t, resp2.Header.Get("Idempotency-Replayed"),
		"Idempotency-Replayed must not be set when no store is configured")
}
