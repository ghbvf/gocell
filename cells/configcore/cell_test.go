package configcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/slices/configpublish"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

func newTestCell() *ConfigCore {
	return NewConfigCore(
		WithClock(clock.Real()),
		WithConfigRepository(mem.NewConfigRepository(clock.Real())),
		WithFlagRepository(mem.NewFlagRepository(clock.Real())),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithMetricsProvider(metrics.NopProvider{}),
	)
}

// newTestRecorder returns a RegistryRecorder for demo mode with an empty config.
func newTestRecorder() *cell.RegistryRecorder {
	return cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
}

func TestConfigCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()

	// Init
	require.NoError(t, c.Init(ctx, recorder))
	assert.Equal(t, 6, len(c.OwnedSlices()), "should have 6 slices")

	// Start
	require.NoError(t, c.Start(ctx))
	assert.Equal(t, "healthy", c.Health().Status)
	assert.True(t, c.Ready())

	// Stop
	require.NoError(t, c.Stop(ctx))
	assert.Equal(t, "unhealthy", c.Health().Status)
	assert.False(t, c.Ready())
}

func TestConfigCore_Metadata(t *testing.T) {
	c := newTestCell()

	assert.Equal(t, "configcore", c.ID())
	assert.Equal(t, cellvocab.CellTypeCore, c.Type())
	assert.Equal(t, cellvocab.L2, c.ConsistencyLevel())
	assert.Equal(t, "platform", c.Metadata().Owner.Team)
}

func TestConfigCore_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestConfigCore_InitDemoMode_RejectsHalfConfiguredPath(t *testing.T) {
	checkHalfConfigured := func(t *testing.T, c *ConfigCore) {
		t.Helper()
		err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
		require.Error(t, err)
		var ecErrHalf *errcode.Error
		require.True(t, errors.As(err, &ecErrHalf))
		assert.Contains(t, ecErrHalf.Message+" "+ecErrHalf.InternalMessage, "outboxWriter and txRunner")
	}

	t.Run("writer without tx manager", func(t *testing.T) {
		c := NewConfigCore(
			WithClock(clock.Real()),
			WithInMemoryDefaults(),
			WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
			WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		)
		checkHalfConfigured(t, c)
	})

	t.Run("tx manager without writer", func(t *testing.T) {
		c := NewConfigCore(
			WithClock(clock.Real()),
			WithInMemoryDefaults(),
			WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
			WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		)
		checkHalfConfigured(t, c)
	})
}

func TestConfigCore_InitDurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField("version"))),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, ecErr.Message+" "+ecErr.InternalMessage, "durable mode")
}

func TestConfigCore_InitDemoMode_NoPublisherNoOutbox_Fails(t *testing.T) {
	c := NewConfigCore(WithClock(clock.Real()), WithInMemoryDefaults())
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	var ecErrSink *errcode.Error
	require.True(t, errors.As(err, &ecErrSink))
	assert.Contains(t, ecErrSink.Message+" "+ecErrSink.InternalMessage, "explicit event sink")
}

func TestConfigCore_InitDemoMode_WithPublisher_Succeeds(t *testing.T) {
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.NoError(t, err)
}

func TestConfigCore_InitDemoMode_ExplicitNoopOutboxPair_Succeeds(t *testing.T) {
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.NoError(t, err)
}

// TestConfigCoreInit_WithEmitter_DirectInjection mirrors the accesscore
// WithEmitter test: a pre-composed emitter skips cell.ResolveEmitter.
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed client.
func TestConfigCoreInit_WithEmitter_DirectInjection(t *testing.T) {
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithEmitter(outbox.NewNoopEmitter()),
	)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)))
	assert.NotNil(t, c.emitter)
	assert.Nil(t, c.pendingOutboxPub)
	assert.Nil(t, c.pendingOutboxWriter)
}

// TestConfigCoreInit_WithEmitterAndOutboxDeps_MutuallyExclusive guards
// against setting both paths at once.
func TestConfigCoreInit_WithEmitterAndOutboxDeps_MutuallyExclusive(t *testing.T) {
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithEmitter(outbox.NewNoopEmitter()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err)
	var ecErrMutex *errcode.Error
	require.True(t, errors.As(err, &ecErrMutex))
	assert.Contains(t, ecErrMutex.Message+" "+ecErrMutex.InternalMessage, "mutually exclusive")
}

// TestConfigCoreInit_WithEmitter_DurableRequiresDurableEmitter guards the
// durable-mode safety invariant: directly-injected non-durable emitter must
// be rejected in DurabilityDurable mode.
func TestConfigCoreInit_WithEmitter_DurableRequiresDurableEmitter(t *testing.T) {
	cursorCodec, err := query.NewCursorCodec([]byte("cfg-wrapper-durable-test-key!!!!"))
	require.NoError(t, err)
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithCursorCodec(cursorCodec),
		WithEmitter(outbox.NewNoopEmitter()), // non-durable
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
	)
	err = c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable))
	require.Error(t, err)
	var ecErrDurable *errcode.Error
	require.True(t, errors.As(err, &ecErrDurable))
	assert.Contains(t, ecErrDurable.Message+" "+ecErrDurable.InternalMessage, "durable")
}

func TestConfigCore_RouteGroups(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	require.Len(t, snap.RouteGroups, 2, "configcore should declare 2 route groups (primary + internal)")

	// Locate groups by listener type — resilient to codegen ordering changes.
	var primary, internal *cell.RouteGroup
	for i := range snap.RouteGroups {
		g := &snap.RouteGroups[i]
		if g.Listener == cell.PrimaryListener {
			primary = g
		}
		if g.Listener == cell.InternalListener {
			internal = g
		}
	}
	require.NotNil(t, primary, "expected primary listener route group")
	require.NotNil(t, internal, "expected internal listener route group")

	assert.Equal(t, "/api/v1", primary.Prefix)
	assert.NotNil(t, primary.Register)
	assert.Equal(t, "/internal/v1", internal.Prefix)
	assert.NotNil(t, internal.Register)

	// Verify the register function actually mounts routes.
	mux := &stubMux{}
	require.NoError(t, primary.Register(mux))
	assert.GreaterOrEqual(t, mux.handleCount, 2, "should register at least 2 route patterns")

	internalMux := &stubMux{}
	require.NoError(t, internal.Register(internalMux))
	assert.GreaterOrEqual(t, internalMux.handleCount, 1, "internal group should register at least 1 route pattern")
}

func TestConfigCore_RegisterSubscriptions(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	require.Len(t, snap.Subscriptions, 2,
		"configcore registers entry-upserted + entry-deleted state-sync handlers")

	// Collect topics as a set — cell_gen.go sorts alphabetically, so positional
	// assertions would be brittle. Verify both topics and their consumer groups.
	topics := make(map[string]string, 2)
	for _, sub := range snap.Subscriptions {
		topics[sub.Spec.Topic] = sub.ConsumerGroup
	}
	// New codegen pattern: Topic == ContractID after PR-CODEGEN-FULL-MIGRATION-FU.
	assert.Equal(t, "configcore", topics["event.config.entry-upserted.v1"],
		"entry-upserted subscription must be registered with configcore consumer group")
	assert.Equal(t, "configcore", topics["event.config.entry-deleted.v1"],
		"entry-deleted subscription must be registered with configcore consumer group")
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

// initCellWithRouter creates an initialized ConfigCore with routes registered
// on a real chi-based router, ready for HTTP testing.
// Routes are mounted via the registry snapshot (Batch 3 declarative API).
func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
	for _, rg := range snap.RouteGroups {
		rg := rg
		if rg.Prefix != "" {
			r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
		} else {
			require.NoError(t, rg.Register(r))
		}
	}
	require.NoError(t, r.FinalizeAuth())
	return r
}

func TestConfigCore_RouteConfigList(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/", nil)
	req = req.WithContext(auth.TestContext("tester", []string{"admin"}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/config/ with admin context should return 200 (got %d)", rec.Code)
}

func TestConfigCore_RouteConfigCreate(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"key":"app.name","value":"test"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"POST /api/v1/config/ should not return 404 (got %d)", rec.Code)
}

func TestConfigCore_RouteConfigGetByKey(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/app.name", nil)
	req = req.WithContext(auth.TestContext("tester", []string{"admin"}))
	r.ServeHTTP(rec, req)

	// Handler ran (not routing 404): response must be JSON and must not be an auth rejection.
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json",
		"GET /api/v1/config/{key} should return JSON (route matched), got plain text (routing 404)")
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
		"GET /api/v1/config/{key} with admin context must not be 401; got body %s", rec.Body)
	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"GET /api/v1/config/{key} with admin context must not be 403; got body %s", rec.Body)
}

func TestConfigCore_RouteFlagsList(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/", nil)
	req = req.WithContext(auth.TestContext("tester", []string{"admin"}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/flags/ with admin context should return 200 (got %d)", rec.Code)
}

// TestConfigCore_ProductionAuthGateLock is the P0 integration test demanded by
// the PR review: it exercises the REAL production routing path (cell.go ->
// slice.RegisterRoutes -> auth.Mount) and locks the 401 / 403 / 2xx
// spectrum end-to-end for each admin-guarded write endpoint.
//
// This test would have caught the prior drift where cell.go attached raw
// HandlerFuncs that bypassed the route-level policy — every case below depends
// on the admin guard actually being wired on the production path.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer_test.go — integration
// test reaches the installed handler via the real mux so authz wiring is
// verified, not stubbed.
func TestConfigCore_ProductionAuthGateLock(t *testing.T) {
	r := initCellWithRouter(t)

	type adminWritePath struct {
		name   string
		method string
		path   string
		body   string
	}
	paths := []adminWritePath{
		{"config-read:list", http.MethodGet, "/api/v1/config/", ""},
		{"config-read:get", http.MethodGet, "/api/v1/config/k", ""},
		{"flag-read:list", http.MethodGet, "/api/v1/flags/", ""},
		{"flag-read:get", http.MethodGet, "/api/v1/flags/k", ""},
		{"flag-read:evaluate", http.MethodPost, "/api/v1/flags/k/evaluate", `{"subject":"test"}`},
		{"config-write:create", http.MethodPost, "/api/v1/config/", `{"key":"k","value":"v"}`},
		{"config-write:update", http.MethodPut, "/api/v1/config/k", `{"value":"v"}`},
		{"config-write:delete", http.MethodDelete, "/api/v1/config/k", ``},
		{"config-publish:publish", http.MethodPost, "/api/v1/config/k/publish", ``},
		{"config-publish:rollback", http.MethodPost, "/api/v1/config/k/rollback", `{"version":1}`},
		{"flag-write:create", http.MethodPost, "/api/v1/flags/", `{"key":"k","enabled":false,"rolloutPercentage":0,"description":"d"}`},
		{"flag-write:update", http.MethodPut, "/api/v1/flags/k", `{"enabled":true,"rolloutPercentage":10,"description":"d"}`},
		{"flag-write:toggle", http.MethodPost, "/api/v1/flags/k/toggle", `{"enabled":true}`},
		{"flag-write:delete", http.MethodDelete, "/api/v1/flags/k", ``},
	}

	exec := func(t *testing.T, p adminWritePath, ctx context.Context) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(p.method, p.path, strings.NewReader(p.body))
		req.Header.Set("Content-Type", "application/json")
		if ctx != nil {
			req = req.WithContext(ctx)
		}
		r.ServeHTTP(rec, req)
		return rec
	}

	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			// --- 401: no authenticated subject on context.
			rec := exec(t, p, context.Background())
			assert.Equal(t, http.StatusUnauthorized, rec.Code,
				"unauthenticated %s %s must be 401 (auth.Mount -> Authenticated); got body %s",
				p.method, p.path, rec.Body)

			// --- 403: authenticated but wrong role.
			rec = exec(t, p, auth.TestContext("user-non-admin", []string{"viewer"}))
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"non-admin %s %s must be 403 (auth.Mount -> AnyRole(admin)); got body %s",
				p.method, p.path, rec.Body)

			// --- 2xx: admin role. We do not pin the exact success code
			// (some paths return 404 because the resource was not seeded),
			// but 401 / 403 must be gone — proving the policy ran and admits
			// the admin.
			rec = exec(t, p, auth.TestContext("admin-user", []string{"admin"}))
			assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
				"admin %s %s must not be 401; body %s", p.method, p.path, rec.Body)
			assert.NotEqual(t, http.StatusForbidden, rec.Code,
				"admin %s %s must not be 403; body %s", p.method, p.path, rec.Body)
		})
	}
}

func TestConfigCore_CrossSliceCursorRejection(t *testing.T) {
	r := initCellWithRouter(t)

	// Seed enough config entries to produce a nextCursor.
	for i := range 3 {
		body := fmt.Sprintf(`{"key":"cfg-%d","value":"val-%d"}`, i, i)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(auth.TestContext("admin-test", []string{"admin"}))
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusCreated, rec.Code, "setup: create config entry %d", i)
	}

	// Get config-read page with limit=1 to obtain a cursor.
	// config-read declares auth.AnyRole(dto.RoleAdmin) so an admin principal is required.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/?limit=1", nil)
	req = req.WithContext(auth.TestContext("tester", []string{"admin"}))
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var configPage struct {
		NextCursor string `json:"nextCursor"`
		HasMore    bool   `json:"hasMore"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&configPage))
	require.True(t, configPage.HasMore, "need hasMore to get a cursor")
	require.NotEmpty(t, configPage.NextCursor)

	// Use config-read cursor on feature-flag list endpoint — must be rejected.
	// feature-flag also declares auth.AnyRole(dto.RoleAdmin) so supply an admin principal.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/flags/?cursor="+configPage.NextCursor, nil)
	req = req.WithContext(auth.TestContext("tester", []string{"admin"}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"config-read cursor must be rejected by feature-flag endpoint")
}

func TestConfigCore_CrossSliceCursorRejection_Reverse(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
	for _, rg := range snap.RouteGroups {
		rg := rg
		if rg.Prefix != "" {
			r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
		} else {
			require.NoError(t, rg.Register(r))
		}
	}
	require.NoError(t, r.FinalizeAuth())

	// Seed flags directly via repository (no HTTP create endpoint for flags).
	for i := range 3 {
		require.NoError(t, c.flagRepo.Create(ctx, &domain.FeatureFlag{
			ID:      fmt.Sprintf("id-%d", i),
			Key:     fmt.Sprintf("flag-%d", i),
			Type:    domain.FlagBoolean,
			Enabled: true,
		}))
	}

	// Get flag page with limit=1 to obtain a cursor.
	// feature-flag declares auth.AnyRole(dto.RoleAdmin) so an admin principal is required.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/?limit=1", nil)
	req = req.WithContext(auth.TestContext("tester", []string{"admin"}))
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var flagPage struct {
		NextCursor string `json:"nextCursor"`
		HasMore    bool   `json:"hasMore"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&flagPage))
	require.True(t, flagPage.HasMore, "need hasMore to get a flag cursor")

	// Use flag cursor on config-read endpoint — must be rejected.
	// config-read also declares auth.AnyRole(dto.RoleAdmin) so supply an admin principal.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/config/?cursor="+flagPage.NextCursor, nil)
	req = req.WithContext(auth.TestContext("tester", []string{"admin"}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"feature-flag cursor must be rejected by config-read endpoint")
}

// TestConfigCore_InitDurable_RejectsMissingCursorCodec locks the fail-fast
// behavior introduced with RunMode wiring: a durable assembly that forgets
// to inject a production cursor codec must not silently fall back to the
// public demo key baked into the source tree.
func TestConfigCore_InitDurable_RejectsMissingCursorCodec(t *testing.T) {
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithConfigRepository(mem.NewConfigRepository(clock.Real())),
		WithFlagRepository(mem.NewFlagRepository(clock.Real())),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(&recordingConfigWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})), // non-Nooper; durable-gated CheckNotNoop passes
		WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField("version"))),
		// No WithCursorCodec — durable mode must refuse the demo fallback.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
	assert.Contains(t, err.Error(), "cursor codec")
}

// durableTxRunner is a TxRunner that does NOT advertise Noop(); configcore's
// durable-mode init check rejects persistence.NoopTxRunner and accepts this.
// Used by tests that exercise durable-mode behavior without spinning up a
// real database.
type durableTxRunner struct{}

func (durableTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = durableTxRunner{}

// recordingConfigWriter is a minimal outbox.Writer test double that is not a
// Nooper — durable mode requires a non-noop writer.
type recordingConfigWriter struct{ entries []outbox.Entry }

func (w *recordingConfigWriter) Write(_ context.Context, entry outbox.Entry) error {
	w.entries = append(w.entries, entry)
	return nil
}

func mustNewCfgCodec(t *testing.T, key []byte) *query.CursorCodec {
	t.Helper()
	codec, err := query.NewCursorCodec(key)
	require.NoError(t, err)
	return codec
}

// TestConfigCore_DurableInit_WithInjectedRepositories verifies the root
// configcore package remains port-oriented: durable storage is injected as
// repositories, while adapter-specific construction lives outside this package.
// The test verifies:
//  1. pendingOutboxWriter is set by WithOutboxDeps before Init.
//  2. Init() succeeds when repositories are injected via port-level options.
func TestConfigCore_DurableInit_WithInjectedRepositories(t *testing.T) {
	writer := &recordingConfigWriter{}
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithConfigRepository(mem.NewConfigRepository(clock.Real())),
		WithFlagRepository(mem.NewFlagRepository(clock.Real())),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})), // non-Nooper; durable-gated CheckNotNoop passes
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), outbox.WrapWriterForCell(writer)),
		WithCursorCodec(mustNewCfgCodec(t, []byte("wiring-test-cfg-cursor-key-32b!!"))),
		WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField("version"))),
	)
	// Writer is accumulated into pendingOutboxWriter pre-Init.
	assert.NotNil(t, c.pendingOutboxWriter, "WithOutboxDeps must populate pendingOutboxWriter")
	// Init must succeed with explicitly injected repos.
	require.NoError(t, c.Init(t.Context(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable)))
	assert.NotNil(t, c.configRepo, "configRepo must be non-nil after Init")
	assert.NotNil(t, c.flagRepo, "flagRepo must be non-nil after Init")
}

// S10: deriveModes translates DurabilityMode into independent RunMode and
// PublishFailureMode at Init() time.
func TestConfigCore_DeriveModes(t *testing.T) {
	tests := []struct {
		name        string
		durability  cell.DurabilityMode
		wantRunMode query.RunMode
		wantPubMode configpublish.PublishFailureMode
	}{
		{
			name:        "demo maps to demo+fail-open",
			durability:  cell.DurabilityDemo,
			wantRunMode: query.RunModeDemo,
			wantPubMode: configpublish.PublishFailureModeFailOpen,
		},
		{
			name:        "durable maps to prod+fail-closed",
			durability:  cell.DurabilityDurable,
			wantRunMode: query.RunModeProd,
			wantPubMode: configpublish.PublishFailureModeFailClosed,
		},
		{
			name:        "unknown maps to prod+fail-closed (safe default)",
			durability:  cell.DurabilityMode(99),
			wantRunMode: query.RunModeProd,
			wantPubMode: configpublish.PublishFailureModeFailClosed,
		},
	}

	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
	)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runMode, publishMode := c.deriveModes(tt.durability)
			assert.Equal(t, tt.wantRunMode, runMode)
			assert.Equal(t, tt.wantPubMode, publishMode)
		})
	}
}

// TestConfigCore_HealthCheckers_WithDirectEmitter verifies that after Init
// with a DirectEmitter-backed publisher, the registry snapshot contains the
// outbox-failopen-rate checker scoped to "configcore".
func TestConfigCore_HealthCheckers_WithDirectEmitter(t *testing.T) {
	c := newTestCell()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(context.Background(), recorder))

	snap := recorder.Snapshot()
	const emitterKey = "outbox-failopen-rate.configcore"
	require.Contains(t, snap.HealthCheckers, emitterKey, "DirectEmitter health checker must be aggregated")
	assert.NoError(t, snap.HealthCheckers[emitterKey](context.Background()), "fresh emitter should be healthy")
}

// TestConfigCore_HealthCheckers_ConfigRepoReady verifies that after Init the
// registry snapshot contains the differentiated config repo readiness probe
// registered via cell.RegisterRepoReadiness and that the mem-backed probe
// returns nil (always-ready MemStore convention).
func TestConfigCore_HealthCheckers_ConfigRepoReady(t *testing.T) {
	c := newTestCell()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(context.Background(), recorder))

	snap := recorder.Snapshot()
	const probeKey = "config_repo_ready"
	require.Contains(t, snap.HealthCheckers, probeKey,
		"RegisterRepoReadiness must register config_repo_ready in the registry snapshot")
	assert.NoError(t, snap.HealthCheckers[probeKey](context.Background()),
		"mem-backed config_repo_ready must return nil (MemStore always-ready convention)")
}

// TestConfigCore_HealthCheckers_NilEmitter verifies that when the emitter does
// not implement the health-checker interface, no emitter-scoped health checkers
// are registered. The config_repo_ready probe is always present (it is
// unconditionally registered via cell.RegisterRepoReadiness); only the
// outbox-failopen-rate probe is absent when the emitter has no HealthCheckers.
func TestConfigCore_HealthCheckers_NilEmitter(t *testing.T) {
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithEmitter(outbox.NewNoopEmitter()), // WriterEmitter — no HealthCheckers method
	)
	recorder := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), recorder))
	snap := recorder.Snapshot()
	assert.NotContains(t, snap.HealthCheckers, "outbox-failopen-rate.configcore",
		"WriterEmitter must not register outbox-failopen-rate probe")
	assert.Contains(t, snap.HealthCheckers, "config_repo_ready",
		"config_repo_ready must always be registered via RegisterRepoReadiness")
}

// ---------------------------------------------------------------------------
// PR464 P2.1 follow-up: cover phase0 CAS-protocol rejection paths so
// regression catches a composition root that wires typed-nil or omits CAS in
// durable mode.
// ---------------------------------------------------------------------------

// TestConfigCore_WithCASProtocol_TypedNil_RejectedAtInit verifies that passing
// a typed-nil *cas.Protocol via WithCASProtocol sets the sticky sentinel and
// phase0 rejects with ErrCellInvalidConfig.
func TestConfigCore_WithCASProtocol_TypedNil_RejectedAtInit(t *testing.T) {
	var typedNil *cas.Protocol // typed-nil
	c := NewConfigCore(
		WithClock(clock.Real()),
		WithInMemoryDefaults(),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),
		WithCASProtocol(typedNil),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err, "typed-nil *cas.Protocol must be rejected at phase0 sentinel")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, ec.Message, "typed-nil *cas.Protocol rejected",
		"phase0 diagnostic must name the typed-nil sentinel branch")
}

// TestConfigCore_DurableMode_MissingCASProtocol_FailsFast verifies that durable
// mode requires WithCASProtocol; omitting it causes phase0 to fail at the
// early CAS check (before other dependency validations).
func TestConfigCore_DurableMode_MissingCASProtocol_FailsFast(t *testing.T) {
	c := NewConfigCore(WithClock(clock.Real()))
	// DurabilityDurable + no WithCASProtocol must fail at phase0 CAS check.
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, ec.Message, "durable mode requires a CAS protocol",
		"diagnostic must point operators at the missing CAS wiring")
}
