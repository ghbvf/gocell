package bootstrap

// primary_authorizer_test.go — table-driven tests for WithPrimaryAuthorizer and
// the authorizerInjector middleware installation.
//
// Coverage goals:
//   - WithPrimaryAuthorizer(nil)           → phase0 fail-fast (nil sentinel)
//   - WithPrimaryAuthorizer(typed-nil)     → phase0 fail-fast (validation.IsNilInterface)
//   - Primary listener installs injector   → AuthorizerFromContext returns the wired authorizer
//   - Internal listener does NOT install   → AuthorizerFromContext returns ok=false
//   - Health listener does NOT install     → AuthorizerFromContext returns ok=false
//   - No authorizer configured             → primary listener does not inject one

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	routerpkg "github.com/ghbvf/gocell/framework/runtime/http/router"
)

// newMinimalAsm returns a bare-minimum CoreAssembly for unit tests that only
// need to drive buildListenerRouterOpts (which calls s.asm.CellIDs() once).
func newMinimalAsm(id string) *assembly.CoreAssembly {
	return assembly.New(clock.Real(), assembly.Config{ID: id, DurabilityMode: outbox.DurabilityDemo})
}

// stubAuthorizer is a minimal auth.Authorizer for bootstrap wiring tests.
type stubAuthorizer struct{ id string }

func (s *stubAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return authz.Allow(authz.Obligations{})
}

// ─── nil/typed-nil fail-fast ──────────────────────────────────────────────────

// TestWithPrimaryAuthorizer_NilBare_FailsFastAtPhase0 verifies that passing a
// bare nil to WithPrimaryAuthorizer is recorded as a sentinel and rejected by
// phase0ValidateOptions before any component starts.
func TestWithPrimaryAuthorizer_NilBare_FailsFastAtPhase0(t *testing.T) {
	t.Parallel()

	b := New(
		clock.Real(),
		WithListener(cell.PrimaryListener, ":8080", []kauth.ListenerAuth{kauth.AuthNone{}}),
		WithListener(cell.HealthListener, ":9091", []kauth.ListenerAuth{kauth.AuthNone{}}),
		WithPrimaryAuthorizer(nil),
	)

	err := b.phase0ValidateOptions()
	require.Error(t, err, "bare-nil authorizer must be rejected at phase0")
	assert.True(t,
		strings.Contains(err.Error(), "authorizer") || strings.Contains(err.Error(), "Authorizer"),
		"error must mention authorizer; got: %v", err)
}

// TestWithPrimaryAuthorizer_TypedNil_FailsFastAtPhase0 verifies that a typed-nil
// (non-nil interface holding a nil pointer) is treated identically to bare nil.
func TestWithPrimaryAuthorizer_TypedNil_FailsFastAtPhase0(t *testing.T) {
	t.Parallel()

	var nilAuthz *stubAuthorizer // typed nil
	b := New(
		clock.Real(),
		WithListener(cell.PrimaryListener, ":8080", []kauth.ListenerAuth{kauth.AuthNone{}}),
		WithListener(cell.HealthListener, ":9091", []kauth.ListenerAuth{kauth.AuthNone{}}),
		WithPrimaryAuthorizer(nilAuthz),
	)

	err := b.phase0ValidateOptions()
	require.Error(t, err, "typed-nil authorizer must be rejected at phase0")
	assert.True(t,
		strings.Contains(err.Error(), "authorizer") || strings.Contains(err.Error(), "Authorizer"),
		"error must mention authorizer; got: %v", err)
}

// ─── buildListenerRouterOpts unit tests (white-box) ───────────────────────────

// buildOptsForRef is a white-box helper that drives buildListenerRouterOpts
// for a single listener ref with an optional authorizer. It uses the same
// minimal bootstrap pattern as newMinimalBootstrap in auth_plan_apply_test.go
// (white-box, same package) and constructs a phaseState via newPhaseState.
func buildOptsForRef(t *testing.T, ref cell.ListenerRef, a auth.Authorizer) []routerpkg.Option {
	t.Helper()

	b := newMinimalBootstrap()
	b.primaryAuthorizer = a

	chain := []kauth.ListenerAuth{kauth.AuthNone{}}
	b.listenerConfigs[ref] = listenerConfig{
		addr:      ":0",
		authChain: chain,
	}

	_, s := newPhaseState()
	// buildListenerRouterOpts calls s.asm.CellIDs(); provide a minimal assembly.
	s.asm = newMinimalAsm("build-opts-test")
	opts, err := b.buildListenerRouterOpts(s, ref, b.listenerConfigs[ref])
	require.NoError(t, err)
	return opts
}

// probePath returns a path suitable for a probe route on the given listener ref.
// Internal listener requires /internal/v1/ prefix; primary/health accept any path.
func probePath(ref cell.ListenerRef) string {
	if ref == cell.InternalListener {
		return "/internal/v1/authz/probe"
	}
	return "/probe/authz"
}

// authorizerPresentAfterOpts applies router options to a real Router, serves a
// single request through it, and returns whether an Authorizer was found in the
// request context via auth.AuthorizerFromContext.
func authorizerPresentAfterOpts(t *testing.T, ref cell.ListenerRef, opts []routerpkg.Option) bool {
	t.Helper()

	rtr, err := routerpkg.NewForListener(clock.Real(), ref, opts...)
	require.NoError(t, err)

	path := probePath(ref)
	var gotAuthorizer bool
	require.NoError(t, auth.Mount(rtr, auth.Route{
		Contract: testHTTPContract(http.MethodGet, path),
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, gotAuthorizer = auth.AuthorizerFromContext(r.Context())
		}),
		Public: true,
	}))
	require.NoError(t, rtr.FinalizeAuth())

	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return gotAuthorizer
}

// TestBuildListenerRouterOpts_PrimaryInstallsAuthorizerInjector verifies that
// when an authorizer is configured and the listener is PrimaryListener,
// buildListenerRouterOpts produces router options that install the injector
// middleware — a request through the built router has an Authorizer retrievable
// via auth.AuthorizerFromContext.
func TestBuildListenerRouterOpts_PrimaryInstallsAuthorizerInjector(t *testing.T) {
	t.Parallel()

	a := &stubAuthorizer{id: "primary-pdp"}
	opts := buildOptsForRef(t, cell.PrimaryListener, a)
	assert.True(t, authorizerPresentAfterOpts(t, cell.PrimaryListener, opts),
		"primary listener must have Authorizer in request context after WithPrimaryAuthorizer is wired")
}

// TestBuildListenerRouterOpts_InternalDoesNotInstallAuthorizerInjector verifies
// that the injector middleware is NOT installed on the InternalListener.
func TestBuildListenerRouterOpts_InternalDoesNotInstallAuthorizerInjector(t *testing.T) {
	t.Parallel()

	a := &stubAuthorizer{id: "primary-pdp"}
	opts := buildOptsForRef(t, cell.InternalListener, a)
	assert.False(t, authorizerPresentAfterOpts(t, cell.InternalListener, opts),
		"internal listener must NOT have Authorizer injected")
}

// TestBuildListenerRouterOpts_HealthDoesNotInstallAuthorizerInjector verifies
// that the injector middleware is NOT installed on the HealthListener.
func TestBuildListenerRouterOpts_HealthDoesNotInstallAuthorizerInjector(t *testing.T) {
	t.Parallel()

	a := &stubAuthorizer{id: "primary-pdp"}
	opts := buildOptsForRef(t, cell.HealthListener, a)
	assert.False(t, authorizerPresentAfterOpts(t, cell.HealthListener, opts),
		"health listener must NOT have Authorizer injected")
}

// TestBuildListenerRouterOpts_PrimaryNoAuthorizerConfigured verifies that
// when no authorizer is configured, the primary listener does NOT inject one.
func TestBuildListenerRouterOpts_PrimaryNoAuthorizerConfigured(t *testing.T) {
	t.Parallel()

	opts := buildOptsForRef(t, cell.PrimaryListener, nil)
	assert.False(t, authorizerPresentAfterOpts(t, cell.PrimaryListener, opts),
		"primary listener must not inject Authorizer when none is configured")
}

// ─── end-to-end integration: request through a running Bootstrap ──────────────

// authzProbeCell mounts a single GET /api/v1/authz/probe route that records
// whether an Authorizer was present in the request context.
type authzProbeCell struct {
	*cell.BaseCell
	gotAuthorizer chan bool
}

func newAuthzProbeCell() *authzProbeCell {
	return &authzProbeCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID:   "authzprobe",
			Type: "core",
		}),
		gotAuthorizer: make(chan bool, 1),
	}
}

func (c *authzProbeCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	reg.RouteGroup(cell.RouteGroup{
		Listener: cell.PrimaryListener,
		Prefix:   "",
		Register: func(mux cell.RouteMux) error {
			mustMount(mux, auth.Route{
				Contract: testHTTPContract(http.MethodGet, "/api/v1/authz/probe"),
				Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					_, ok := auth.AuthorizerFromContext(r.Context())
					select {
					case c.gotAuthorizer <- ok:
					default:
					}
				}),
				Public: true,
			})
			return nil
		},
	})
	return nil
}

// TestWithPrimaryAuthorizer_EndToEnd_PrimaryInjectsAuthorizer is an
// end-to-end integration test: a real Bootstrap with WithPrimaryAuthorizer wired
// must deliver the Authorizer in the request context on the primary listener.
func TestWithPrimaryAuthorizer_EndToEnd_PrimaryInjectsAuthorizer(t *testing.T) {
	primaryLn := newLocalListener(t)
	healthLn := newLocalListener(t)

	c := newAuthzProbeCell()
	asm := assembly.New(clock.Real(), assembly.Config{ID: "authzprobe-e2e", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	a := &stubAuthorizer{id: "pdp-e2e"}
	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.HealthListener, healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}}, WithListenerNet(healthLn)),
		WithPrimaryAuthorizer(a),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	testwait.External(t, "bootstrap-healthy", func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", healthLn.Addr().String()))
		if err != nil {
			return false
		}
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "health listener did not become ready")

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/authz/probe", primaryLn.Addr().String()))
	require.NoError(t, err)
	closeBody(t, resp)

	select {
	case ok := <-c.gotAuthorizer:
		assert.True(t, ok, "primary listener handler must find Authorizer in request context")
	default:
		t.Fatal("handler was not invoked")
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestWithPrimaryAuthorizer_EndToEnd_InternalDoesNotInjectAuthorizer verifies
// that the internal listener does NOT receive the Authorizer in context even
// when WithPrimaryAuthorizer is configured.
func TestWithPrimaryAuthorizer_EndToEnd_InternalDoesNotInjectAuthorizer(t *testing.T) {
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)
	healthLn := newLocalListener(t)

	internalGot := make(chan bool, 1)
	internalAuthChain, internalRing := testInternalAuthChain(t)

	probingCell := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		func(w http.ResponseWriter, r *http.Request) {
			_, ok := auth.AuthorizerFromContext(r.Context())
			select {
			case internalGot <- ok:
			default:
			}
			w.WriteHeader(http.StatusOK)
		},
	)

	asm := assembly.New(clock.Real(), assembly.Config{ID: "authz-internal-no-inject", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Register(probingCell))

	a := &stubAuthorizer{id: "pdp-e2e-internal"}
	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(),
			internalAuthChain, WithListenerNet(internalLn)),
		WithListener(cell.HealthListener, healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}}, WithListenerNet(healthLn)),
		WithPrimaryAuthorizer(a),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	testwait.External(t, "bootstrap-healthy", func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", healthLn.Addr().String()))
		if err != nil {
			return false
		}
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "health listener did not become ready")

	resp := getWithServiceToken(t,
		fmt.Sprintf("http://%s/internal/v1/admin/ping", internalLn.Addr().String()),
		internalRing)
	closeBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	select {
	case ok := <-internalGot:
		assert.False(t, ok, "internal listener handler must NOT find Authorizer in request context")
	default:
		t.Fatal("internal handler was not invoked")
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}
}
