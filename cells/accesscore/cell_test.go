package accesscore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
)

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
	testKeySet, _, _ = auth.MustNewTestKeySet(clock.Real())
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
	clock := storetest.NewFakeClock(time.Now())
	return refreshmem.MustNew(refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clock, nil)
}

func newTestCell(t testing.TB) *AccessCore {
	t.Helper()
	return NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
}

func newDurableTestCell(t testing.TB) *AccessCore {
	t.Helper()
	return NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithCursorCodec(testCursorCodec),
		WithOutboxDeps(nil, durableOutboxWriter{}),
		WithTxManager(durableTxRunner{}),
	)
}

func TestAccessCore_Init_RequiresJWTIssuer(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTVerifier(testVerifier), // issuer missing
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
}

func TestAccessCore_Init_RequiresJWTVerifier(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer), // verifier missing
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}

func TestAccessCore_Init_RequiresRepositoriesBeforeSliceConstruction(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithMetricsProvider(metrics.NopProvider{}),
	)

	var err error
	require.NotPanics(t, func() {
		err = c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user repository")
}

func TestInit_DemoMode_OutboxWithoutTx_Fails(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		// txRunner intentionally omitted
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outboxWriter and txRunner")
}

func TestInit_DemoMode_TxWithoutOutbox_PublisherMode_Succeeds(t *testing.T) {
	// Publisher-only mode with txRunner: txRunner is for slice services (required
	// since B-1 deleted NoopTxRunner); it is not passed to the emitter resolver
	// so the writer/txRunner pairing invariant is not violated. Init must succeed.
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
		// outboxWriter intentionally omitted — publisher-only mode
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.NoError(t, err)
}

func TestInit_TxRunnerXOR_BothPresent(t *testing.T) {
	// Both outboxWriter and txRunner present → should succeed
	c := newTestCell(t) // newTestCell includes both
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))
}

func TestInit_DemoMode_NoPublisherNoOutbox_Fails(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicit event sink")
}

func TestInit_DemoMode_WithPublisher_Succeeds(t *testing.T) {
	// L2 cell, both nil, but publisher present → OK (demo mode with warning)
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.NoError(t, err)
}

func TestInit_DemoMode_ExplicitNoopOutboxPair_Succeeds(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.NoError(t, err)
}

func TestInitRefreshGC_DisabledAndConfigValidation(t *testing.T) {
	c := NewAccessCore()
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
			c := NewAccessCore(WithClock(clock.Real()), WithRefreshGC(tc.interval, tc.retention))
			err := c.initRefreshGC()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestAccessCore_InitWithRefreshGCRegistersLifecycleHook(t *testing.T) {
	c := newTestCell(t)
	WithRefreshGC(time.Hour, time.Hour)(c)

	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
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
	c := NewAccessCore()
	hook := c.refreshGCHook()

	require.NoError(t, hook.OnStop(context.Background()))
	assert.Nil(t, c.refreshGC)
}

func TestAccessCore_RefreshGCHookStartPropagatesWorkerConfigError(t *testing.T) {
	c := NewAccessCore(WithClock(clock.Real()), WithRefreshGC(time.Hour, time.Hour))
	c.refreshGCCollector = refresh.NoopGCCollector{}
	hook := c.refreshGCHook()

	err := hook.OnStart(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "store is required")
	assert.Nil(t, c.refreshGC)
}

// TestInit_WithEmitter_DirectInjection exercises the F3 WithEmitter path:
// a pre-composed outbox.Emitter skips cell.ResolveEmitter entirely.
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed client.
func TestInit_WithEmitter_DirectInjection(t *testing.T) {
	emitter := outbox.NewNoopEmitter()
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithEmitter(emitter),
		WithTxManager(durableTxRunner{}),
	)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))
	// After Init the cell holds the injected emitter; pending raw deps stay nil.
	assert.NotNil(t, c.emitter)
	assert.Nil(t, c.pendingOutboxPub)
	assert.Nil(t, c.pendingOutboxWriter)
}

// TestInit_WithEmitterAndOutboxDeps_MutuallyExclusive guards against wiring
// mistakes where a composition root accidentally sets both paths.
func TestInit_WithEmitterAndOutboxDeps_MutuallyExclusive(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithEmitter(outbox.NewNoopEmitter()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestInit_WithEmitter_DurableRequiresDurableEmitter guards the production
// safety invariant: in DurabilityDurable mode, direct-injected emitters must
// be durable. Injecting a NoopEmitter (non-durable) in durable mode is a
// wiring mistake that would silently downgrade L2 atomicity.
func TestInit_WithEmitter_DurableRequiresDurableEmitter(t *testing.T) {
	c := NewAccessCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithCursorCodec(testCursorCodec),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithEmitter(outbox.NewNoopEmitter()), // non-durable
		WithTxManager(durableTxRunner{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "durable")
}

func TestAccessCore_Lifecycle(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()

	// Init
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))
	assert.Equal(t, 10, len(c.OwnedSlices()), "should have 10 slices")

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
	assert.Equal(t, cell.CellTypeCore, c.Type())
	assert.Equal(t, cell.L2, c.ConsistencyLevel())
}

func TestAccessCore_Startup(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestAccessCore_TokenVerifierAndAuthorizer(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))

	assert.NotNil(t, c.TokenVerifier())
	assert.NotNil(t, c.Authorizer())
}

func TestAccessCore_Init_DurableMode_UsesProdRBACRunMode(t *testing.T) {
	c := newDurableTestCell(t)
	ctx := context.Background()
	reg := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable)
	require.NoError(t, c.Init(ctx, reg))

	snap := reg.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
	for _, rg := range snap.RouteGroups {
		if rg.Listener == cell.PrimaryListener {
			rg := rg
			r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
		}
	}
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/usr-1?cursor=not-a-valid-cursor", nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccessCore_RouteGroups(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	groups := snap.RouteGroups
	require.Len(t, groups, 2, "accesscore should declare 2 route groups")

	// First group: PrimaryListener at /api/v1/access.
	assert.Equal(t, cell.PrimaryListener, groups[0].Listener)
	assert.Equal(t, "/api/v1/access", groups[0].Prefix)
	assert.NotNil(t, groups[0].Register)
	primaryMux := &stubMux{}
	require.NoError(t, groups[0].Register(primaryMux))
	assert.GreaterOrEqual(t, primaryMux.handleCount, 3, "primary group should register at least 3 routes")

	// Second group: InternalListener at /internal/v1/access.
	assert.Equal(t, cell.InternalListener, groups[1].Listener)
	assert.Equal(t, "/internal/v1/access", groups[1].Prefix)
	assert.NotNil(t, groups[1].Register)
	internalMux := &stubMux{}
	require.NoError(t, groups[1].Register(internalMux))
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
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	primary := router.MustNew(router.WithRouterClock(clock.Real()))
	internal := router.MustNew(router.WithRouterClock(clock.Real()))
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
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
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
	req = req.WithContext(auth.TestContext("user-1", []string{"viewer"}))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_FORBIDDEN")
}

func TestAccessCore_RouteSessionLogout(t *testing.T) {
	r := initCellWithRouter(t)

	// Path params under /api/v1/access/sessions/{id} are declared
	// `format: uuid` in the contract; non-existent but well-formed UUIDs reach
	// the handler and return 404. SelfOr("id", RoleAdmin) policy: subject
	// must match the path {id} or carry the admin role. Use the same session
	// UUID as subject so the policy admits the request.
	const nonexistentSessionID = "00000000-0000-4000-8000-000000000099"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/access/sessions/"+nonexistentSessionID, nil)
	req = req.WithContext(auth.TestContext(nonexistentSessionID, nil))
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
	req = req.WithContext(auth.TestContext(nonexistentUserID, nil)) // self-access
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/access/users/{id} should reach handler (got %d)", rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not chi 404)")
}

func TestAccessCore_RouteRoleAssign(t *testing.T) {
	r := initCellWithRouters(t).Internal

	// Role "admin" is not seeded in newTestCell(t) → domain-level 404 (role not found).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign",
		strings.NewReader(`{"userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{auth.RoleInternalAdmin}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not router 404)")
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_ROLE_NOT_FOUND")
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
	req = req.WithContext(auth.TestContext("user-1", []string{"viewer"}))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_FORBIDDEN")
}

func TestAccessCore_RouteRoleRevoke(t *testing.T) {
	r := initCellWithRouters(t).Internal

	// Revoking a role that the user does not hold is an idempotent no-op → 200.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/revoke",
		strings.NewReader(`{"userId":"usr-1","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{auth.RoleInternalAdmin}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not router 404)")
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
	req = req.WithContext(auth.TestContext("user-1", []string{"viewer"}))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ERR_AUTH_FORBIDDEN")
}

func TestAccessCore_RouteRolesList(t *testing.T) {
	r := initCellWithRouter(t)

	const userID = "00000000-0000-4000-8000-000000000097"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+userID, nil)
	req = req.WithContext(auth.TestContext(userID, nil)) // self-access
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
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()

	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(userRepo),
		WithSessionRepository(sessionRepo),
		WithRoleRepository(roleRepo),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	ctx := context.Background()
	reg := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(ctx, reg))

	// Seed a user.
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.DefaultCost)
	user, err := domain.NewUser("e2e-user", "e2e@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-e2e"
	require.NoError(t, userRepo.Create(ctx, user))

	// Login via HTTP handler to simulate real flow.
	snap := reg.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
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
	claims, err := verifier.VerifyIntent(ctx, accessToken, auth.TokenIntentAccess)
	require.NoError(t, err, "token should be valid before revocation")

	sid := claims.SessionID
	require.NotEmpty(t, sid, "token must contain sid claim")
	_, sidParseErr := uuid.Parse(sid)
	require.NoError(t, sidParseErr, "session id must be a canonical UUID (PR-A45)")

	// Revoke the session.
	sess, err := sessionRepo.GetByID(ctx, sid)
	require.NoError(t, err)
	sess.Revoke(time.Now())
	require.NoError(t, sessionRepo.Update(ctx, sess))

	// Verify same token again — should be rejected.
	_, err = verifier.VerifyIntent(ctx, accessToken, auth.TokenIntentAccess)
	require.Error(t, err, "token should be rejected after session revocation")
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN", "error should be auth invalid token")
}

// TestAccessCore_RefreshTokenRevocation_E2E verifies the refresh→validate→revoke
// chain: login → refresh → validate refreshed token → revoke → verify rejected.
func TestAccessCore_RefreshTokenRevocation_E2E(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()

	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(userRepo),
		WithSessionRepository(sessionRepo),
		WithRoleRepository(roleRepo),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	ctx := context.Background()
	reg := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(ctx, reg))

	// Seed a user.
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.DefaultCost)
	user, err := domain.NewUser("refresh-user", "refresh@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-refresh"
	require.NoError(t, userRepo.Create(ctx, user))

	// Login via HTTP.
	snap := reg.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
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
	claims, err := verifier.VerifyIntent(ctx, refreshedToken, auth.TokenIntentAccess)
	require.NoError(t, err, "refreshed token should be valid")

	sid := claims.SessionID
	require.NotEmpty(t, sid)

	// Revoke the session.
	sess, err := sessionRepo.GetByID(ctx, sid)
	require.NoError(t, err)
	sess.Revoke(time.Now())
	require.NoError(t, sessionRepo.Update(ctx, sess))

	// Refreshed token should now be rejected.
	_, err = verifier.VerifyIntent(ctx, refreshedToken, auth.TokenIntentAccess)
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
	hash, err := bcrypt.GenerateFromPassword([]byte(password), domain.BcryptCost)
	require.NoError(t, err)

	user, err := domain.NewUser(username, username+"@gocell.local", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-admin-prefill"

	require.NoError(t, roleRepo.Create(ctx, &domain.Role{
		ID:   auth.RoleAdmin,
		Name: auth.RoleAdmin,
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	}))
	require.NoError(t, userRepo.Create(ctx, user))
	_, err = roleRepo.AssignToUser(ctx, user.ID, auth.RoleAdmin)
	require.NoError(t, err)
	return user
}

// TestAccessCore_DirectPrefill_AdminRoleAndUser verifies that a cell can be
// initialized when the admin role and user are pre-filled directly into repos
// (equivalent to the old WithSeedAdmin fixture pattern for integration tests).
func TestAccessCore_DirectPrefill_AdminRoleAndUser(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	ctx := context.Background()

	seedAdminUser(t, ctx, userRepo, roleRepo, "admin", "admin-pass-123")

	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(userRepo),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(roleRepo),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	require.NoError(t, c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))

	// Admin role exists.
	role, err := roleRepo.GetByID(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", role.Name)

	// Admin user exists.
	user, err := userRepo.GetByUsername(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "usr-admin-prefill", user.ID)

	// Password is hashed at the shared BcryptCost.
	hashCost, err := bcrypt.Cost([]byte(user.PasswordHash))
	require.NoError(t, err)
	assert.Equal(t, domain.BcryptCost, hashCost)

	// Role assigned.
	roles, err := roleRepo.GetByUserID(ctx, user.ID)
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
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	ctx := context.Background()
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(ctx, rec))

	snap := rec.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
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

// noopPublisher implements eventbus.Publisher for tests that do not care
// about published events. Keeps AccessCore.Init happy in demo mode.
type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (noopPublisher) Close(_ context.Context) error                       { return nil }

var _ outbox.Publisher = noopPublisher{}
