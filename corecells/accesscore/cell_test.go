package accesscore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/framework/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh/storetest"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	"github.com/ghbvf/gocell/framework/runtime/http/router"
)

// testTenantID is the canonical test tenant UUID used in cell_test.go.
var testTenantID = func() tenant.TenantID {
	t, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		panic("cell_test: invalid testTenantID: " + err.Error())
	}
	return t
}()

// withTenant injects the canonical test tenant into a context.
func withTenant(ctx context.Context) context.Context {
	return ctxkeys.WithTenantID(ctx, "00000000-0000-0000-0000-000000000001")
}

// --- ABAC PDP test doubles (PR-10c #1348) ---
//
// Local to package accesscore: accesscoretest imports the accesscore cell, so it
// cannot be imported from package accesscore without a cycle. Verdict construction
// (authz.Allow/Deny) lives in this _test.go per AUTHZ-DECISION-ALLOW-DENY-CALLER-01.

// capturingTestAuthorizer is a test auth.Authorizer that returns a fixed Decision
// and records the action the gate asked the PDP for (action-pin in
// TestAccessCore_ProductionAuthGateLock).
type capturingTestAuthorizer struct {
	decision  authz.Decision
	gotAction string
}

func (c *capturingTestAuthorizer) Authorize(_ context.Context, _, _, action string) (authz.Decision, error) {
	c.gotAction = action
	return c.decision, nil
}

func allowCapturingAuthorizer() *capturingTestAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("test allowCapturingAuthorizer: authz.Allow: " + err.Error())
	}
	return &capturingTestAuthorizer{decision: dec}
}

// withAllowAuthorizer wires an allow PDP into ctx — the production composition
// root installs the real Authorizer on the primary listener, so route tests that
// hit a permission-gated endpoint as admin need one or RequirePermission fails closed.
func withAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, allowCapturingAuthorizer())
}

func withDenyAuthorizer(ctx context.Context, reason string) context.Context {
	return auth.WithAuthorizer(ctx, &capturingTestAuthorizer{decision: authz.Deny(reason)})
}

// durableTxRunner is a test-only TxRunner that simulates a non-noop (real) tx
// context. It does NOT hold mem.Store.mu, so it injects no lease: repo methods
// take their per-call lock (race-safe, no cross-method atomicity). For
// whole-closure atomicity wire mem.Store.TxRunner() instead. See ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md.
type durableTxRunner struct{}

func (durableTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = durableTxRunner{}

type durableOutboxWriter struct{}

func (durableOutboxWriter) Write(_ context.Context, _ outbox.Entry) error { return nil }

var _ outbox.Writer = durableOutboxWriter{}

// testPassword is a fixed test-only credential used to seed users in E2E tests.
// Not a real secret — safe to appear in test source code.
const testPassword = "secret123"

var (
	testKeySet, _, _ = keystest.MustNewKeySet(clock.Real())
	testIssuer       = mustIssuer(testKeySet)
	testVerifier     = mustVerifier(testKeySet)
	testCursorCodec  = mustCursorCodec()
)

func mustIssuer(ks *auth.KeySet) *auth.JWTIssuer {
	i, err := auth.NewJWTIssuer(ks, "gocell-accesscore", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return i
}

func mustVerifier(ks *auth.KeySet) *auth.JWTVerifier {
	v, err := auth.NewJWTVerifier(ks, clock.Real(), auth.WithExpectedAudiences("gocell"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return v
}

func mustCursorCodec() *query.CursorCodec {
	codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return codec
}

func newTestRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

func newTestCell(t testing.TB) *AccessCore {
	t.Helper()
	return NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
}

func newDurableTestCell(t testing.TB) *AccessCore {
	t.Helper()
	return NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithCursorCodec(testCursorCodec),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(durableOutboxWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
}

func TestAccessCore_Init_RequiresJWTIssuer(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTVerifier(testVerifier), // issuer missing
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
}

func TestAccessCore_Init_RequiresJWTVerifier(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer), // verifier missing
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}

func TestAccessCore_Init_RequiresRepositoriesBeforeSliceConstruction(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)

	var err error
	require.NotPanics(t, func() {
		err = c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user repository")
}

func TestInit_DemoMode_OutboxWithoutTx_Fails(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		// txRunner intentionally omitted
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err)
	var ecErrTxPair *errcode.Error
	require.True(t, errors.As(err, &ecErrTxPair))
	assert.Contains(t, ecErrTxPair.Message+" "+ecErrTxPair.Error(), "outboxWriter and txRunner")
}

func TestInit_DemoMode_TxWithoutOutbox_PublisherMode_Succeeds(t *testing.T) {
	// Publisher-only mode with txRunner: txRunner is for slice services (required
	// since B-1 deleted NoopTxRunner); it is not passed to the emitter resolver
	// so the writer/txRunner pairing invariant is not violated. Init must succeed.
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		// outboxWriter intentionally omitted — publisher-only mode
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.NoError(t, err)
}

func TestInit_TxRunnerXOR_BothPresent(t *testing.T) {
	// Both outboxWriter and txRunner present → should succeed
	c := newTestCell(t) // newTestCell includes both
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)))
}

func TestInit_DemoMode_NoPublisherNoOutbox_Fails(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err)
	var ecErrSink *errcode.Error
	require.True(t, errors.As(err, &ecErrSink))
	assert.Contains(t, ecErrSink.Message+" "+ecErrSink.Error(), "explicit event sink")
}

func TestInit_DemoMode_WithPublisher_Succeeds(t *testing.T) {
	// L2 cell, both nil, but publisher present → OK (demo mode with warning)
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.NoError(t, err)
}

func TestInit_DemoMode_ExplicitNoopOutboxPair_Succeeds(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.NoError(t, err)
}

func TestInitRefreshGC_DisabledAndConfigValidation(t *testing.T) {
	c := NewAccessCore(clock.Real(), withTestCASProtocol())
	require.NoError(t, c.initRefreshGC())
	assert.Nil(t, c.refreshGCCollector)

	tests := []struct {
		name      string
		interval  time.Duration
		retention time.Duration
		want      string
	}{
		{name: "interval must be positive", interval: 0, retention: time.Hour, want: "interval must be positive"},
		{name: "retention must be positive", interval: time.Hour, retention: 0, want: "retention must be positive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewAccessCore(clock.Real(), WithRefreshGC(tc.interval, tc.retention), withTestCASProtocol())
			err := c.initRefreshGC()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestAccessCore_InitWithRefreshGCRegistersLifecycleHook(t *testing.T) {
	c := newTestCell(t)
	WithRefreshGC(time.Hour, time.Hour)(c)

	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	require.NotNil(t, c.refreshGCCollector)

	snap := rec.Snapshot()
	require.Len(t, snap.LifecycleHooks, 1)
	hook := snap.LifecycleHooks[0]
	assert.Equal(t, "accesscore.refresh-gc", hook.Name)

	require.NoError(t, hook.OnStart(context.Background()))
	assert.NotNil(t, c.refreshGC)
	require.NoError(t, hook.OnStop(context.Background()))
	assert.Nil(t, c.refreshGC)
}

func TestAccessCore_RefreshGCHookStopWithoutStartNoops(t *testing.T) {
	c := NewAccessCore(clock.Real(), withTestCASProtocol())
	hook := c.refreshGCHook()

	require.NoError(t, hook.OnStop(context.Background()))
	assert.Nil(t, c.refreshGC)
}

func TestAccessCore_RefreshGCHookStartPropagatesWorkerConfigError(t *testing.T) {
	c := NewAccessCore(clock.Real(), WithRefreshGC(time.Hour, time.Hour), withTestCASProtocol())
	c.refreshGCCollector = refresh.NoopGCCollector{}
	hook := c.refreshGCHook()

	err := hook.OnStart(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "store is required")
	assert.Nil(t, c.refreshGC)
}

// TestInit_WithEmitter_DirectInjection exercises the F3 WithEmitter path:
// a pre-composed outbox.Emitter skips outbox.ResolveEmitter entirely.
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed client.
func TestInit_WithEmitter_DirectInjection(t *testing.T) {
	emitter := outbox.DemoCellEmitter()
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithEmitter(emitter),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)))
	// After Init the cell holds the injected emitter; pending raw deps stay nil.
	assert.NotNil(t, c.emitter)
	assert.Nil(t, c.pendingOutboxPub)
	assert.Nil(t, c.pendingOutboxWriter)
}

// TestInit_WithEmitterAndOutboxDeps_MutuallyExclusive guards against wiring
// mistakes where a composition root accidentally sets both paths.
func TestInit_WithEmitterAndOutboxDeps_MutuallyExclusive(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithEmitter(outbox.DemoCellEmitter()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo))
	require.Error(t, err)
	var ecErrMutex *errcode.Error
	require.True(t, errors.As(err, &ecErrMutex))
	assert.Contains(t, ecErrMutex.Message+" "+ecErrMutex.Error(), "mutually exclusive")
}

// TestInit_WithEmitter_DurableRequiresDurableEmitter guards the production
// safety invariant: in DurabilityDurable mode, direct-injected emitters must
// be durable. Injecting a NoopEmitter (non-durable) in durable mode is a
// wiring mistake that would silently downgrade L2 atomicity.
func TestInit_WithEmitter_DurableRequiresDurableEmitter(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithCursorCodec(testCursorCodec),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithEmitter(outbox.DemoCellEmitter()), // non-durable
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable))
	require.Error(t, err)
	var ecErrDurable *errcode.Error
	require.True(t, errors.As(err, &ecErrDurable))
	assert.Contains(t, ecErrDurable.Message+" "+ecErrDurable.Error(), "durable")
}

func TestAccessCore_Lifecycle(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()

	// Init
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)))
	assert.Equal(t, 12, len(c.OwnedSlices()), "should have 12 slices")

	// Start
	require.NoError(t, c.Start(ctx))
	assert.Equal(t, "healthy", c.Health().Status)
	assert.True(t, c.Ready())

	// Stop
	require.NoError(t, c.Stop(ctx))
	assert.Equal(t, "unhealthy", c.Health().Status)
	assert.False(t, c.Ready())
}

func TestAccessCore_Metadata(t *testing.T) {
	c := newTestCell(t)
	assert.Equal(t, "accesscore", c.ID())
	assert.Equal(t, cellvocab.CellTypeCore, c.Type())
	assert.Equal(t, cellvocab.L3, c.ConsistencyLevel())
}

func TestAccessCore_Startup(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestAccessCore_TokenVerifierAndAuthorizer(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)))

	assert.NotNil(t, c.TokenVerifier())
	assert.NotNil(t, c.Authorizer())
}

func TestAccessCore_Init_DurableMode_UsesProdRBACRunMode(t *testing.T) {
	c := newDurableTestCell(t)
	ctx := context.Background()
	reg := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable)
	require.NoError(t, c.Init(ctx, reg))

	snap := reg.Snapshot()
	r, err := router.New(clock.Real())
	require.NoError(t, err)
	for _, rg := range snap.RouteGroups {
		if rg.Listener == cell.PrimaryListener {
			rg := rg
			r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
		}
	}
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/usr-1?cursor=not-a-valid-cursor", nil)
	// Wire the PDP (admin → baseline allow) as the composition root does; the
	// migrated rbaccheck gate (auth.RequirePermissionForResource, #1977 Batch B)
	// fails closed without a wired Authorizer.
	// admin-user != "usr-1" → admin baseline fires → handler runs → 400 (bad cursor/id).
	req = req.WithContext(withAllowAuthorizer(withTenant(auth.TestContext("admin-user", []string{"admin"}))))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccessCore_RouteGroups(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	groups := snap.RouteGroups
	require.Len(t, groups, 2, "accesscore should declare 2 route groups")

	// Locate groups by listener type — resilient to codegen ordering changes.
	var primary, internal *cell.RouteGroup
	for i := range groups {
		g := &groups[i]
		if g.Listener == cell.PrimaryListener {
			primary = g
		}
		if g.Listener == cell.InternalListener {
			internal = g
		}
	}
	require.NotNil(t, primary, "expected primary listener route group")
	require.NotNil(t, internal, "expected internal listener route group")

	// Primary group: PrimaryListener at /api/v1/access.
	assert.Equal(t, "/api/v1/access", primary.Prefix)
	assert.NotNil(t, primary.Register)
	primaryMux := &stubMux{}
	require.NoError(t, primary.Register(primaryMux))
	assert.GreaterOrEqual(t, primaryMux.handleCount, 3, "primary group should register at least 3 routes")

	// Internal group: InternalListener at /internal/v1/access.
	assert.Equal(t, "/internal/v1/access", internal.Prefix)
	assert.NotNil(t, internal.Register)
	internalMux := &stubMux{}
	require.NoError(t, internal.Register(internalMux))
	assert.GreaterOrEqual(t, internalMux.handleCount, 1, "internal group should register at least 1 route")
}

// stubMux implements cell.RouteMux for testing.
type stubMux struct {
	handleCount int
}

func (m *stubMux) Handle(_ string, _ http.Handler) { m.handleCount++ }
func (m *stubMux) Route(_ string, fn func(cell.RouteMux)) {
	m.handleCount++
	fn(m)
}
func (m *stubMux) Mount(_ string, _ http.Handler)                          { m.handleCount++ }
func (m *stubMux) Group(_ func(cell.RouteMux))                             { m.handleCount++ }
func (m *stubMux) With(_ ...func(http.Handler) http.Handler) cell.RouteMux { return m }

// cellTestRouters holds the primary and internal routers built from an
// AccessCore's RouteGroups. Tests for /api/v1/* use Primary; tests for
// /internal/v1/* use Internal.
type cellTestRouters struct {
	Primary  *router.Router
	Internal *router.Router
}

// initCellWithRouters creates an initialized AccessCore with both listener
// routers populated. FinalizeAuth is called on each router.
func initCellWithRouters(t *testing.T) *cellTestRouters {
	t.Helper()
	c := newTestCell(t)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	primary, err := router.New(clock.Real())
	require.NoError(t, err)
	internal, err := router.New(clock.Real())
	require.NoError(t, err)
	for _, rg := range snap.RouteGroups {
		rg := rg
		switch rg.Listener {
		case cell.PrimaryListener:
			primary.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
		case cell.InternalListener:
			internal.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
		}
	}
	require.NoError(t, primary.FinalizeAuth())
	require.NoError(t, internal.FinalizeAuth())
	return &cellTestRouters{Primary: primary, Internal: internal}
}

// initCellWithRouter creates an initialized AccessCore with primary routes
// registered on a real chi-based router, ready for HTTP testing of
// /api/v1/* endpoints. FinalizeAuth is called so the Router accepts ServeHTTP.
func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	return initCellWithRouters(t).Primary
}

func TestAccessCore_RouteSessionLogin(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"username":"alice","password":"secret"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", "00000000-0000-0000-0000-000000000001")
	r.ServeHTTP(rec, req)

	// We expect a non-404 status. The exact status depends on business logic
	// (e.g. 401 for bad credentials), but 404 means routing is broken.
	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"POST /api/v1/access/sessions/login should not return 404 (got %d)", rec.Code)
}

func TestAccessCore_RouteSessionRefresh(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"refreshToken":"tok"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/refresh", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", "00000000-0000-0000-0000-000000000001")
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"POST /api/v1/access/sessions/refresh should not return 404 (got %d)", rec.Code)
}

func TestAccessCore_RouteUserCreate(t *testing.T) {
	r := initCellWithRouter(t)

	// Admin creates user → 201.
	body := `{"username":"bob","email":"bob@example.com","password":"secret123"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/users/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// admin + wired PDP (baseline allow) → user:write granted → 201.
	req = req.WithContext(withAllowAuthorizer(withTenant(auth.TestContext("admin-user", []string{"admin"}))))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code,
		"POST /api/v1/access/users/ with admin should return 201 (got %d)", rec.Code)
}

func TestAccessCore_RouteUserCreate_NoAuth_Returns401(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/users/",
		strings.NewReader(`{"username":"x","email":"x@y.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	// No auth context.
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_UNAUTHORIZED")
}

func TestAccessCore_RouteUserCreate_NonAdmin_Returns403(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/users/",
		strings.NewReader(`{"username":"x","email":"x@y.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withTenant(auth.TestContext("user-1", []string{"viewer"})))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_FORBIDDEN")
}

// TestAccessCore_ProductionAuthGateLock exercises the REAL production routing path
// (cell.go -> slice.RegisterRoutes -> auth.Mount) and locks the
// 401 / 403(fail-closed) / 403(PDP-deny) / 2xx spectrum for every permission-gated
// accesscore endpoint after the #1977 Batch B migration (auth.RequirePermission /
// auth.RequirePermissionForResource). It is the single authoritative home for the
// endpoint↔permission action-pin: the role-agnostic baseline allows admin for every
// accesscore perm, so a wrong-permission misbinding (e.g. a write endpoint demanding
// user:read) would pass the slice tests AND the archtest silently — only the
// per-endpoint wantAction assertion here catches it.
//
// For the ownership-gated endpoints (selfExempt) it pins that a caller naming
// ITSELF in the path is admitted WITH a wired allow-Authorizer (self is now a PDP
// baseline ownership rule — subject.sub == resource.id — not a Go short-circuit).
// Without an Authorizer even self-naming requests are denied (fail-closed).
func TestAccessCore_ProductionAuthGateLock(t *testing.T) {
	r := initCellWithRouter(t)

	const (
		otherID = "00000000-0000-0000-0000-0000000000aa" // target != caller → non-self → PDP path
		selfID  = "00000000-0000-0000-0000-0000000000bb" // caller subject for the self-exemption case
	)

	type gate struct {
		name       string
		method     string
		path       string // {id}/{userID} already substituted with otherID
		selfPath   string // path with selfID substituted; set only when selfExempt
		body       string
		wantAction string
		selfExempt bool
	}
	gates := []gate{
		{name: "policy-read:list", method: http.MethodGet, path: "/api/v1/access/policies", wantAction: "policy:read"},
		{name: "policy-read:get", method: http.MethodGet, path: "/api/v1/access/policies/" + otherID, wantAction: "policy:read"},
		{name: "policy-write:create", method: http.MethodPost, path: "/api/v1/access/policies", body: `{}`, wantAction: "policy:write"},
		{name: "policy-write:update", method: http.MethodPut, path: "/api/v1/access/policies/" + otherID, body: `{}`, wantAction: "policy:write"},
		{name: "policy-write:delete", method: http.MethodDelete, path: "/api/v1/access/policies/" + otherID, wantAction: "policy:write"},
		{name: "user-write:create", method: http.MethodPost, path: "/api/v1/access/users", body: `{}`, wantAction: "user:write"},
		{
			name: "user-read:get", method: http.MethodGet,
			path: "/api/v1/access/users/" + otherID, selfPath: "/api/v1/access/users/" + selfID,
			wantAction: "user:read", selfExempt: true,
		},
		{
			name: "user-write:update", method: http.MethodPut,
			path: "/api/v1/access/users/" + otherID, selfPath: "/api/v1/access/users/" + selfID,
			body: `{}`, wantAction: "user:write", selfExempt: true,
		},
		{
			name: "user-write:patch", method: http.MethodPatch,
			path: "/api/v1/access/users/" + otherID, selfPath: "/api/v1/access/users/" + selfID,
			body: `{}`, wantAction: "user:write", selfExempt: true,
		},
		{name: "user-write:delete", method: http.MethodDelete, path: "/api/v1/access/users/" + otherID, wantAction: "user:write"},
		{
			name: "user-write:lock", method: http.MethodPost,
			path: "/api/v1/access/users/" + otherID + "/lock", body: `{}`, wantAction: "user:write",
		},
		{
			name: "user-write:unlock", method: http.MethodPost,
			path: "/api/v1/access/users/" + otherID + "/unlock", body: `{}`, wantAction: "user:write",
		},
		{
			name: "user-write:change-password", method: http.MethodPost,
			path: "/api/v1/access/users/" + otherID + "/password", selfPath: "/api/v1/access/users/" + selfID + "/password",
			body: `{}`, wantAction: "user:write", selfExempt: true,
		},
		{
			name: "role-read:list", method: http.MethodGet,
			path: "/api/v1/access/roles/" + otherID, selfPath: "/api/v1/access/roles/" + selfID,
			wantAction: "role:read", selfExempt: true,
		},
		{
			name: "role-read:check", method: http.MethodGet,
			path: "/api/v1/access/roles/" + otherID + "/admin", selfPath: "/api/v1/access/roles/" + selfID + "/admin",
			wantAction: "role:read", selfExempt: true,
		},
	}

	exec := func(t *testing.T, g gate, path string, ctx context.Context) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(g.method, path, strings.NewReader(g.body))
		req.Header.Set("Content-Type", "application/json")
		if ctx != nil {
			req = req.WithContext(ctx)
		}
		r.ServeHTTP(rec, req)
		return rec
	}
	adminCtx := func() context.Context {
		return withTenant(auth.TestContext("admin-user", []string{"admin"}))
	}

	for _, g := range gates {
		t.Run(g.name, func(t *testing.T) {
			// 401: no authenticated subject (both gate kinds → 401 with no principal).
			rec := exec(t, g, g.path, context.Background())
			assert.Equal(t, http.StatusUnauthorized, rec.Code,
				"unauthenticated %s %s must be 401; body %s", g.method, g.path, rec.Body)

			// 403 fail-closed: admin (non-self, target=otherID) but NO Authorizer wired.
			rec = exec(t, g, g.path, adminCtx())
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"%s %s with no Authorizer must fail closed (403); body %s", g.method, g.path, rec.Body)

			// 403 PDP-deny: authenticated non-admin (non-self) + wired PDP that DENIES.
			rec = exec(t, g, g.path, withDenyAuthorizer(
				withTenant(auth.TestContext("user-non-admin", []string{"viewer"})), "policy: no matching allow rule",
			))
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"%s %s denied by PDP must be 403; body %s", g.method, g.path, rec.Body)

			// 2xx + action-pin: admin (non-self) + capturing PDP that ALLOWS. We do not
			// pin the exact success code (some paths 400/404 on the empty body / unseeded
			// resource), but 401/403 must be gone and the gate must have demanded the
			// correct permission string.
			cap := allowCapturingAuthorizer()
			rec = exec(t, g, g.path, auth.WithAuthorizer(adminCtx(), cap))
			assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "admin %s %s (PDP allow) must not be 401; body %s", g.method, g.path, rec.Body)
			assert.NotEqual(t, http.StatusForbidden, rec.Code, "admin %s %s (PDP allow) must not be 403; body %s", g.method, g.path, rec.Body)
			assert.Equal(t, g.wantAction, cap.gotAction,
				"route %s %s must request PDP action %q (got %q); a misbinding would be masked by "+
					"the role-agnostic baseline that allows admin for every accesscore perm",
				g.method, g.path, g.wantAction, cap.gotAction)

			// Ownership gate: a caller naming ITSELF in the path is admitted WITH a
			// wired allow-Authorizer (self is now a PDP baseline ownership rule,
			// subject.sub == resource.id, via RequirePermissionForResource — #1977 Batch B).
			// Without an Authorizer even self-naming callers are denied (fail-closed).
			if g.selfExempt {
				// With allow-Authorizer: self-naming caller reaches the handler (no 401/403).
				rec = exec(t, g, g.selfPath,
					auth.WithAuthorizer(
						withTenant(auth.TestContext(selfID, []string{"viewer"})),
						allowCapturingAuthorizer(),
					),
				)
				assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
					"self %s %s with Authorizer must not be 401 (ownership rule grants); body %s",
					g.method, g.selfPath, rec.Body)
				assert.NotEqual(t, http.StatusForbidden, rec.Code,
					"self %s %s with Authorizer must not be 403 (ownership rule grants); body %s",
					g.method, g.selfPath, rec.Body)

				// Without Authorizer: fail-closed (403), even for self-naming caller.
				rec = exec(t, g, g.selfPath, withTenant(auth.TestContext(selfID, []string{"viewer"})))
				assert.Equal(t, http.StatusForbidden, rec.Code,
					"self %s %s without Authorizer must be 403 (fail-closed, #1977 Batch B); body %s",
					g.method, g.selfPath, rec.Body)
			}
		})
	}
}

func TestAccessCore_RouteSessionLogout(t *testing.T) {
	r := initCellWithRouter(t)

	// Path params under /api/v1/access/sessions/{id} are declared
	// `format: uuid` in the contract; non-existent but well-formed UUIDs reach
	// the handler and return 404. There is no route-level SelfOr gate: {id}
	// is a session id (not a user id), so ownership is enforced inside the
	// Service by comparing the JWT subject against the session's user_id.
	// Use the same UUID as subject to satisfy the baseline JWT auth (valid
	// principal) without triggering a 401 from missing-subject check.
	const nonexistentSessionID = "00000000-0000-4000-8000-000000000099"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/access/sessions/"+nonexistentSessionID, nil)
	req = req.WithContext(withTenant(auth.TestContext(nonexistentSessionID, nil)))
	r.ServeHTTP(rec, req)

	// 404 means handler was reached and session not found (correct routing).
	// 403/405 or chi-level 404 (without JSON body) means routing is broken.
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"DELETE /api/v1/access/sessions/{id} should reach handler (got %d)", rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not chi 404)")
}

func TestAccessCore_RouteUserGet(t *testing.T) {
	r := initCellWithRouter(t)

	const nonexistentUserID = "00000000-0000-4000-8000-000000000098"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+nonexistentUserID, nil)
	// Self-access now requires a wired Authorizer (RequirePermissionForResource, #1977 Batch B).
	req = req.WithContext(withAllowAuthorizer(withTenant(auth.TestContext(nonexistentUserID, nil))))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/access/users/{id} should reach handler (got %d)", rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not chi 404)")
}

func TestAccessCore_RouteRoleAssign(t *testing.T) {
	r := initCellWithRouters(t).Internal

	// #1337 PR-3b: tenantId is supplied in the request body by the service-token
	// caller (InternalListener, no JWT). usr-1 is not seeded in newTestCell(t), so
	// the user lookup fails → domain-level 404 (user not found). The role-not-found
	// path is covered at the rbacassign slice level (TestService_Assign, with a
	// seeded user).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign",
		strings.NewReader(`{"tenantId":"00000000-0000-0000-0000-000000000001","userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	// Service principals carry no JWT tenant; tenant is supplied via request body.
	req = req.WithContext(auth.TestServiceContext("accesscore"))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not router 404)")
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_USER_NOT_FOUND")
}

func TestAccessCore_RouteRoleAssign_NoAuth_Returns401(t *testing.T) {
	r := initCellWithRouters(t).Internal

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign",
		strings.NewReader(`{"userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	// No auth context.
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_UNAUTHORIZED")
}

func TestAccessCore_RouteRoleAssign_NonAdmin_Returns403(t *testing.T) {
	r := initCellWithRouters(t).Internal

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign",
		strings.NewReader(`{"userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withTenant(auth.TestContext("user-1", []string{"viewer"})))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_FORBIDDEN")
}

func TestAccessCore_RouteRoleRevoke(t *testing.T) {
	r := initCellWithRouters(t).Internal

	// #1337 PR-3b: tenantId is supplied in the request body by the service-token
	// caller (InternalListener, no JWT). usr-1 is not seeded in newTestCell(t), so
	// the target-tenant ownership guard (review F4) fails the user lookup →
	// domain-level 404 (user not found), SYMMETRIC with the Assign route above.
	// Before F4, Revoke skipped this check and returned a misleading 200
	// revoked:true for a user absent from the tenant (the role survived in its
	// real tenant); the guard turns that silent no-op into a clean 404. The
	// idempotent role-not-held no-op (user EXISTS, role absent → 200) is covered at
	// the rbacassign slice level (TestRevoke_NoOp_*, with a seeded user).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/revoke",
		strings.NewReader(`{"tenantId":"00000000-0000-0000-0000-000000000001","userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	// Service principals carry no JWT tenant; tenant is supplied via request body.
	req = req.WithContext(auth.TestServiceContext("accesscore"))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not router 404)")
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_USER_NOT_FOUND")
}

func TestAccessCore_RouteRoleRevoke_NoAuth_Returns401(t *testing.T) {
	r := initCellWithRouters(t).Internal

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/revoke",
		strings.NewReader(`{"userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_UNAUTHORIZED")
}

func TestAccessCore_RouteRoleRevoke_NonAdmin_Returns403(t *testing.T) {
	r := initCellWithRouters(t).Internal

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/revoke",
		strings.NewReader(`{"userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withTenant(auth.TestContext("user-1", []string{"viewer"})))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_FORBIDDEN")
}

func TestAccessCore_RouteRolesList(t *testing.T) {
	r := initCellWithRouter(t)

	const userID = "00000000-0000-4000-8000-000000000097"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+userID, nil)
	req = req.WithContext(withTenant(auth.TestContext(userID, nil))) // self-access
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/access/roles/{userID} should not return 404 (got %d)", rec.Code)
	assert.NotEqual(t, http.StatusBadRequest, rec.Code,
		"path param must be a valid UUID (CH-05); got %d body=%s", rec.Code, rec.Body.String())
}

// TestAccessCore_SessionRevocation_E2E verifies the complete session revocation
// chain: login → token has sid → verify ok → revoke → verify rejected.
func TestAccessCore_SessionRevocation_E2E(t *testing.T) {
	// Use separate repos so we can manipulate session state.
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()

	c := NewAccessCore(
		clock.Real(),
		withUserRepository(userRepo),
		WithSessionStore(sessionRepo),
		withRoleRepository(roleRepo),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	ctx := context.Background()
	reg := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(ctx, reg))

	// Seed a user (fixture hash at MinCost — seeded directly into the repo,
	// bypassing the cell hasher; cost is irrelevant to the login flow under test).
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	user, err := domain.NewUser("e2e-user", "e2e@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-e2e"
	require.NoError(t, userRepo.Create(ctx, testTenantID, user))

	// Login via HTTP handler to simulate real flow.
	snap := reg.Snapshot()
	r, err := router.New(clock.Real())
	require.NoError(t, err)
	for _, rg := range snap.RouteGroups {
		rg := rg
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			} else {
				require.NoError(t, rg.Register(r))
			}
		}
	}
	require.NoError(t, r.FinalizeAuth())

	body := fmt.Sprintf(`{"username":"e2e-user","password":%q}`, testPassword)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", "00000000-0000-0000-0000-000000000001")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, "login should succeed: %s", rec.Body.String())

	// Extract access token from response via structured JSON parsing.
	var loginResp struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &loginResp), "should parse login response JSON")
	accessToken := loginResp.Data.AccessToken
	require.NotEmpty(t, accessToken, "login response must contain access token")

	// Verify token through session-aware verifier — should succeed.
	verifier := c.TokenVerifier()
	claims, err := verifier.VerifyIntent(ctx, accessToken, kauth.TokenIntentAccess)
	require.NoError(t, err, "token should be valid before revocation")

	sid := claims.SessionID
	require.NotEmpty(t, sid, "token must contain sid claim")
	_, sidParseErr := uuid.Parse(sid)
	require.NoError(t, sidParseErr, "session id must be a canonical UUID (PR-A45)")

	// Revoke the session.
	require.NoError(t, sessionRepo.Revoke(ctx, sid))

	// Verify same token again — should be rejected.
	_, err = verifier.VerifyIntent(ctx, accessToken, kauth.TokenIntentAccess)
	require.Error(t, err, "token should be rejected after session revocation")
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN", "error should be auth invalid token")
}

// TestAccessCore_RefreshTokenRevocation_E2E verifies the refresh→validate→revoke
// chain: login → refresh → validate refreshed token → revoke → verify rejected.
func TestAccessCore_RefreshTokenRevocation_E2E(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()

	c := NewAccessCore(
		clock.Real(),
		withUserRepository(userRepo),
		WithSessionStore(sessionRepo),
		withRoleRepository(roleRepo),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	ctx := context.Background()
	reg := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(ctx, reg))

	// Seed a user (fixture hash at MinCost — seeded directly into the repo,
	// bypassing the cell hasher; cost is irrelevant to the login flow under test).
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	user, err := domain.NewUser("refresh-user", "refresh@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-refresh"
	require.NoError(t, userRepo.Create(ctx, testTenantID, user))

	// Login via HTTP.
	snap := reg.Snapshot()
	r, err := router.New(clock.Real())
	require.NoError(t, err)
	for _, rg := range snap.RouteGroups {
		rg := rg
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			} else {
				require.NoError(t, rg.Register(r))
			}
		}
	}
	require.NoError(t, r.FinalizeAuth())

	loginBody := fmt.Sprintf(`{"username":"refresh-user","password":%q}`, testPassword)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", strings.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", "00000000-0000-0000-0000-000000000001")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var loginResp struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &loginResp))

	// Refresh via HTTP.
	refreshBody := fmt.Sprintf(`{"refreshToken":%q}`, loginResp.Data.RefreshToken)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/refresh", strings.NewReader(refreshBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", "00000000-0000-0000-0000-000000000001")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "refresh should succeed: %s", rec.Body.String())

	var refreshResp struct {
		Data struct {
			AccessToken string `json:"accessToken"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refreshResp))
	refreshedToken := refreshResp.Data.AccessToken
	require.NotEmpty(t, refreshedToken)

	// Validate refreshed token through session-aware verifier.
	verifier := c.TokenVerifier()
	claims, err := verifier.VerifyIntent(ctx, refreshedToken, kauth.TokenIntentAccess)
	require.NoError(t, err, "refreshed token should be valid")

	sid := claims.SessionID
	require.NotEmpty(t, sid)

	// Revoke the session.
	require.NoError(t, sessionRepo.Revoke(ctx, sid))

	// Refreshed token should now be rejected.
	_, err = verifier.VerifyIntent(ctx, refreshedToken, kauth.TokenIntentAccess)
	require.Error(t, err, "refreshed token should be rejected after session revocation")
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN")
}

// --- Repo-prefill helpers (migrated from WithSeedAdmin/WithSeedAdminRole) ---

// seedAdminUser directly creates an admin user in the given repos without going
// through the bootstrap flow. Used as a test fixture for tests that need
// "there is an admin user" as a precondition.
func seedAdminUser(
	t *testing.T, ctx context.Context,
	userRepo *mem.UserRepository, roleRepo *mem.RoleRepository,
	username, password string,
) *domain.User {
	t.Helper()
	// Fixture hash at MinCost: this seeds a precondition admin directly into the
	// repo (bypassing the cell's hasher), so cost is irrelevant to what's under
	// test; MinCost keeps the fixture fast.
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)

	user, err := domain.NewUser(username, username+"@gocell.local", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-admin-prefill"

	require.NoError(t, roleRepo.Create(ctx, testTenantID, &domain.Role{
		ID:   auth.RoleAdmin,
		Name: auth.RoleAdmin,
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	}))
	require.NoError(t, userRepo.Create(ctx, testTenantID, user))
	_, err = roleRepo.AssignToUser(ctx, testTenantID, user.ID, auth.RoleAdmin)
	require.NoError(t, err)
	return user
}

// TestAccessCore_DirectPrefill_AdminRoleAndUser verifies that a cell can be
// initialized when the admin role and user are pre-filled directly into repos
// (equivalent to the old WithSeedAdmin fixture pattern for integration tests).
func TestAccessCore_DirectPrefill_AdminRoleAndUser(t *testing.T) {
	// Shared store: role repo needs to see the user created by user repo (F4).
	sharedStore := mem.NewStore(clock.Real())
	userRepo := sharedStore.UserRepository()
	roleRepo := sharedStore.RoleRepository()
	ctx := context.Background()

	seedAdminUser(t, ctx, userRepo, roleRepo, "admin", "admin-pass-123")

	c := NewAccessCore(
		clock.Real(),
		withUserRepository(userRepo),
		WithSessionStore(testutil.RealSessionRepo(t)),
		withRoleRepository(roleRepo),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)))

	// Admin role exists.
	role, err := roleRepo.GetByID(ctx, testTenantID, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", role.Name)

	// Admin user exists.
	user, err := userRepo.GetByUsername(ctx, testTenantID, "admin")
	require.NoError(t, err)
	assert.Equal(t, "usr-admin-prefill", user.ID)

	// Password hash is a valid bcrypt hash at the fixture's cost.
	hashCost, err := bcrypt.Cost([]byte(user.PasswordHash))
	require.NoError(t, err)
	assert.Equal(t, bcrypt.MinCost, hashCost)

	// Role assigned.
	roles, err := roleRepo.GetByUserID(ctx, testTenantID, tenant.SystemRowVisibility(), user.ID)
	require.NoError(t, err)
	require.Len(t, roles, 1)
	assert.Equal(t, "admin", roles[0].Name)
}

// TestAccessCore_PasswordResetExempt_PropagatesViaRouter asserts that the
// POST /api/v1/access/users/{id}/password route is declared with
// PasswordResetExempt=true in the Router's auth metadata after RegisterRoutes.
//
// This is the "future regression guardrail" for G3: if identitymanage's
// RegisterRoutes ever loses the PasswordResetExempt attribute (e.g. by drifting
// from a hand-rolled test double), this test will catch it before production.
//
// The test uses DeclaredAuthMetas() which returns metadata accumulated during
// RegisterRoutes, prior to FinalizeAuth compiling the matchers.
func TestAccessCore_PasswordResetExempt_PropagatesViaRouter(t *testing.T) {
	c := NewAccessCore(
		clock.Real(),
		withUserRepository(mem.NewStore(clock.Real()).UserRepository()),
		withRoleRepository(mem.NewStore(clock.Real()).RoleRepository()),
		withPolicyRepository(mem.NewPolicyRepository()),
		withResourceAttributeProvider(mem.NewResourceAttributeProvider()),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		withTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
		withTestCASProtocol(),
		withTestSetupLock(),
		withTestBootstrapAuth(),
	)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	r, err := router.New(clock.Real())
	require.NoError(t, err)
	for _, rg := range snap.RouteGroups {
		rg := rg
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			} else {
				require.NoError(t, rg.Register(r))
			}
		}
	}

	const wantPath = "/api/v1/access/users/{id}/password"
	const wantMethod = "POST"
	var found bool
	for _, m := range r.DeclaredAuthMetas() {
		if m.Method == wantMethod && m.Path == wantPath {
			assert.True(t, m.PasswordResetExempt,
				"POST %s must be declared with PasswordResetExempt=true", wantPath)
			found = true
			break
		}
	}
	require.True(t, found,
		"%s %s must appear in Router.DeclaredAuthMetas(); got %v",
		wantMethod, wantPath, r.DeclaredAuthMetas())
}

// TestAccessCore_RegisterSubscriptions verifies that Init registers the expected
// set of event subscriptions. Mirrors the auditcore/configcore pattern (T-6).
// accesscore subscribes to 4 events: config.entry-deleted, config.entry-upserted,
// role.assigned, role.revoked.
func TestAccessCore_RegisterSubscriptions(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	assert.Equal(t, 4, len(snap.Subscriptions),
		"accesscore registers 4 subscriptions: config.entry-deleted, config.entry-upserted, role.assigned, role.revoked")

	topicSet := make(map[string]bool, len(snap.Subscriptions))
	for _, sub := range snap.Subscriptions {
		topicSet[sub.Spec.Topic] = true
	}
	// Codegen pattern: Topic == ContractID after PR-CODEGEN-FULL-MIGRATION-FU.
	assert.True(t, topicSet["event.config.entry-deleted.v1"],
		"must subscribe to event.config.entry-deleted.v1")
	assert.True(t, topicSet["event.config.entry-upserted.v1"],
		"must subscribe to event.config.entry-upserted.v1")
	assert.True(t, topicSet["event.role.assigned.v1"],
		"must subscribe to event.role.assigned.v1")
	assert.True(t, topicSet["event.role.revoked.v1"],
		"must subscribe to event.role.revoked.v1")
}

// noopPublisher implements eventbus.Publisher for tests that do not care
// about published events. Keeps AccessCore.Init happy in demo mode.
type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (noopPublisher) Close(_ context.Context) error                       { return nil }

var _ outbox.Publisher = noopPublisher{}
