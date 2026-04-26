package configcore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/slices/configpublish"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCell() *ConfigCore {
	return NewConfigCore(
		WithConfigRepository(mem.NewConfigRepository()),
		WithFlagRepository(mem.NewFlagRepository()),
		WithOutboxDeps(eventbus.New(), nil),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
}

func TestConfigCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}

	// Init
	require.NoError(t, c.Init(ctx, deps))
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
	assert.Equal(t, cell.CellTypeCore, c.Type())
	assert.Equal(t, cell.L2, c.ConsistencyLevel())
	assert.Equal(t, "platform", c.Metadata().Owner.Team)
}

func TestConfigCore_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestConfigCore_InitDemoMode_RejectsHalfConfiguredPath(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{
			name: "writer without tx manager",
			opts: []Option{
				WithInMemoryDefaults(),
				WithOutboxDeps(eventbus.New(), nil),
				WithOutboxDeps(nil, outbox.NoopWriter{}),
			},
		},
		{
			name: "tx manager without writer",
			opts: []Option{
				WithInMemoryDefaults(),
				WithOutboxDeps(eventbus.New(), nil),
				WithTxManager(persistence.NoopTxRunner{}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConfigCore(tt.opts...)
			err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "outboxWriter and txRunner")
		})
	}
}

func TestConfigCore_InitDurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithOutboxDeps(eventbus.New(), nil),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDurable,
	}
	err := c.Init(context.Background(), deps)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "durable mode")
}

func TestConfigCore_InitDemoMode_NoPublisherNoOutbox_Fails(t *testing.T) {
	c := NewConfigCore(WithInMemoryDefaults())
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicit event sink")
}

func TestConfigCore_InitDemoMode_WithPublisher_Succeeds(t *testing.T) {
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithOutboxDeps(eventbus.New(), nil),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, err)
}

func TestConfigCore_InitDemoMode_ExplicitNoopOutboxPair_Succeeds(t *testing.T) {
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, err)
}

// TestConfigCoreInit_WithEmitter_DirectInjection mirrors the accesscore
// WithEmitter test: a pre-composed emitter skips cell.ResolveEmitter.
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed client.
func TestConfigCoreInit_WithEmitter_DirectInjection(t *testing.T) {
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithEmitter(outbox.NewNoopEmitter()),
	)
	require.NoError(t, c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}))
	assert.NotNil(t, c.emitter)
	assert.Nil(t, c.pendingOutboxPub)
	assert.Nil(t, c.pendingOutboxWriter)
}

// TestConfigCoreInit_WithEmitterAndOutboxDeps_MutuallyExclusive guards
// against setting both paths at once.
func TestConfigCoreInit_WithEmitterAndOutboxDeps_MutuallyExclusive(t *testing.T) {
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithEmitter(outbox.NewNoopEmitter()),
		WithOutboxDeps(eventbus.New(), nil),
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestConfigCoreInit_WithEmitter_DurableRequiresDurableEmitter guards the
// durable-mode safety invariant: directly-injected non-durable emitter must
// be rejected in DurabilityDurable mode.
func TestConfigCoreInit_WithEmitter_DurableRequiresDurableEmitter(t *testing.T) {
	cursorCodec, err := query.NewCursorCodec([]byte("cfg-wrapper-durable-test-key!!!!"))
	require.NoError(t, err)
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithCursorCodec(cursorCodec),
		WithEmitter(outbox.NewNoopEmitter()), // non-durable
		WithTxManager(persistence.NoopTxRunner{}),
	)
	err = c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDurable})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "durable")
}

func TestConfigCore_RouteGroups(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))

	groups := c.RouteGroups()
	require.Len(t, groups, 1, "configcore should declare 1 route group")
	assert.Equal(t, cell.PrimaryListener, groups[0].Listener)
	assert.Equal(t, "/api/v1", groups[0].Prefix)
	assert.NotNil(t, groups[0].Register)

	// Verify the register function actually mounts routes.
	mux := &stubMux{}
	groups[0].Register(mux)
	assert.GreaterOrEqual(t, mux.handleCount, 2, "should register at least 2 route patterns")
}

func TestConfigCore_RegisterSubscriptions(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))

	r := &celltest.StubEventRouter{}
	require.NoError(t, c.RegisterSubscriptions(r))
	assert.Equal(t, 2, r.HandlerCount(),
		"configcore registers entry-upserted + entry-deleted state-sync handlers")
	assert.Equal(t, []string{
		"event.config.entry-upserted.v1",
		"event.config.entry-deleted.v1",
	}, r.Topics)
	assert.Equal(t, []string{"configcore", "configcore"}, r.ConsumerGroups)
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
// Routes are mounted via RouteGroups (PR-A14b declarative API).
func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))

	r := router.New()
	for _, rg := range c.RouteGroups() {
		if rg.Prefix != "" {
			r.Route(rg.Prefix, func(sub cell.RouteMux) { rg.Register(sub) })
		} else {
			rg.Register(r)
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
		p := p
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
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}
	require.NoError(t, c.Init(ctx, deps))

	r := router.New()
	for _, rg := range c.RouteGroups() {
		if rg.Prefix != "" {
			r.Route(rg.Prefix, func(sub cell.RouteMux) { rg.Register(sub) })
		} else {
			rg.Register(r)
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
		WithConfigRepository(mem.NewConfigRepository()),
		WithFlagRepository(mem.NewFlagRepository()),
		WithOutboxDeps(eventbus.New(), nil),
		WithOutboxDeps(nil, &recordingConfigWriter{}),
		WithTxManager(durableTxRunner{}), // non-Nooper; durable-gated CheckNotNoop passes
		// No WithCursorCodec — durable mode must refuse the demo fallback.
	)
	err := c.Init(context.Background(), cell.Dependencies{
		Config:         map[string]any{},
		DurabilityMode: cell.DurabilityDurable,
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
	assert.Contains(t, err.Error(), "cursor codec")
}

// durableTxRunner is a TxRunner that does NOT advertise Noop(); configcore's
// durable-mode init check rejects persistence.NoopTxRunner and accepts this.
// Used by tests that exercise durable-mode behaviour without spinning up a
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

// TestWithPostgresPool_NilPool_SetsPoolAndDeferred verifies the
// deferred construction contract: WithPostgresPool stores the pool for use
// in Init(). With a nil pool, Init() skips deferred repo construction.
// Outbox wiring is now a separate concern via WithOutboxDeps. The test verifies:
//  1. pendingOutboxWriter is set by WithOutboxDeps before Init.
//  2. pgPool is stored (nil here as sentinel for "no PG path in this test").
//  3. Init() succeeds when configRepo is injected via WithConfigRepository.
func TestWithPostgresPool_NilPool_SetsPoolAndDeferred(t *testing.T) {
	writer := &recordingConfigWriter{}
	c := NewConfigCore(
		WithPostgresPool(nil),                           // nil pool: deferred construction skipped in Init
		WithConfigRepository(mem.NewConfigRepository()), // inject repo directly to satisfy Init
		WithFlagRepository(mem.NewFlagRepository()),
		WithTxManager(durableTxRunner{}), // non-Nooper; durable-gated CheckNotNoop passes
		WithOutboxDeps(eventbus.New(), writer),
		WithCursorCodec(mustNewCfgCodec(t, []byte("wiring-test-cfg-cursor-key-32b!!"))),
	)
	// Writer is accumulated into pendingOutboxWriter pre-Init.
	assert.NotNil(t, c.pendingOutboxWriter, "WithOutboxDeps must populate pendingOutboxWriter")
	// Init must succeed with explicitly injected repos.
	require.NoError(t, c.Init(t.Context(), cell.Dependencies{DurabilityMode: cell.DurabilityDurable}))
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

	c := NewConfigCore(WithInMemoryDefaults(), WithOutboxDeps(eventbus.New(), nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runMode, publishMode := c.deriveModes(tt.durability)
			assert.Equal(t, tt.wantRunMode, runMode)
			assert.Equal(t, tt.wantPubMode, publishMode)
		})
	}
}

// TestConfigCore_HealthCheckers_WithDirectEmitter verifies that after Init
// with a DirectEmitter-backed publisher, HealthCheckers returns the
// outbox-failopen-rate checker scoped to "configcore".
func TestConfigCore_HealthCheckers_WithDirectEmitter(t *testing.T) {
	c := newTestCell()
	deps := cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo}
	require.NoError(t, c.Init(context.Background(), deps))

	checkers := c.HealthCheckers()
	const emitterKey = "outbox-failopen-rate:configcore"
	require.Contains(t, checkers, emitterKey, "DirectEmitter health checker must be aggregated")
	assert.NoError(t, checkers[emitterKey](context.Background()), "fresh emitter should be healthy")
}

// TestConfigCore_HealthCheckers_NilEmitter verifies that HealthCheckers returns
// an empty map when the emitter does not implement cell.HealthContributor.
func TestConfigCore_HealthCheckers_NilEmitter(t *testing.T) {
	c := NewConfigCore() // no emitter set
	checkers := c.HealthCheckers()
	assert.Empty(t, checkers, "nil emitter must produce empty health checkers map")
}
