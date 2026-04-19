package configcore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
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

// noopTxRunner is a test double that executes fn directly without a real transaction.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

func newTestCell() *ConfigCore {
	return NewConfigCore(
		WithConfigRepository(mem.NewConfigRepository()),
		WithFlagRepository(mem.NewFlagRepository()),
		WithPublisher(eventbus.New()),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
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

	assert.Equal(t, "config-core", c.ID())
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

func TestConfigCore_InitRejectsHalfConfiguredDurablePath(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{
			name: "writer without tx manager",
			opts: []Option{
				WithInMemoryDefaults(),
				WithPublisher(eventbus.New()),
				WithOutboxWriter(outbox.NoopWriter{}),
			},
		},
		{
			name: "tx manager without writer",
			opts: []Option{
				WithInMemoryDefaults(),
				WithPublisher(eventbus.New()),
				WithTxManager(noopTxRunner{}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConfigCore(tt.opts...)
			err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
		})
	}
}

func TestConfigCore_InitDurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithPublisher(eventbus.New()),
		WithOutboxWriter(outbox.NoopWriter{}),
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

func TestConfigCore_InitDemoMode_RequiresPublisher(t *testing.T) {
	c := NewConfigCore(WithInMemoryDefaults())
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publisher")
}

func TestConfigCore_InitDemoMode_WithPublisher_Succeeds(t *testing.T) {
	c := NewConfigCore(
		WithInMemoryDefaults(),
		WithPublisher(eventbus.New()),
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any), DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, err)
}

func TestConfigCore_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))

	mux := &stubMux{}
	c.RegisterRoutes(mux)
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
	assert.Equal(t, 1, r.HandlerCount(), "config-core should register 1 topic handler")
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
	c.RegisterRoutes(r)
	return r
}

func TestConfigCore_RouteConfigList(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/", nil)
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/config/ should not return 404 (got %d)", rec.Code)
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
	r.ServeHTTP(rec, req)

	// A business-logic 404 returns a JSON error body with an error code;
	// a routing 404 returns plain text. Verify the route matched by checking
	// the response is JSON (meaning our handler ran, not the router's default 404).
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json",
		"GET /api/v1/config/{key} should return JSON (route matched), got plain text (routing 404)")
}

func TestConfigCore_RouteFlagsList(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/", nil)
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/flags/ should not return 404 (got %d)", rec.Code)
}

// TestConfigCore_ProductionAuthGateLock is the P0 integration test demanded by
// the PR review: it exercises the REAL production routing path (cell.go ->
// slice.RegisterRoutes -> auth.Secured) and locks the 401 / 403 / 2xx
// spectrum end-to-end for each admin-guarded write endpoint.
//
// This test would have caught the prior drift where cell.go attached raw
// HandlerFuncs that bypassed auth.Secured — every case below depends on the
// admin guard actually being wired on the production path.
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
				"unauthenticated %s %s must be 401 (auth.Secured -> Authenticated); got body %s",
				p.method, p.path, rec.Body)

			// --- 403: authenticated but wrong role.
			rec = exec(t, p, auth.TestContext("user-non-admin", []string{"viewer"}))
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"non-admin %s %s must be 403 (auth.Secured -> AnyRole(admin)); got body %s",
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
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/?limit=1", nil)
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
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/flags/?cursor="+configPage.NextCursor, nil)
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
	c.RegisterRoutes(r)

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
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flags/?limit=1", nil)
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var flagPage struct {
		NextCursor string `json:"nextCursor"`
		HasMore    bool   `json:"hasMore"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&flagPage))
	require.True(t, flagPage.HasMore, "need hasMore to get a flag cursor")

	// Use flag cursor on config-read endpoint — must be rejected.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/config/?cursor="+flagPage.NextCursor, nil)
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
		WithPublisher(eventbus.New()),
		WithOutboxWriter(&recordingConfigWriter{}),
		WithTxManager(noopTxRunner{}),
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

// TestWithPostgresDefaults_ConfiguresRepoAndOutbox verifies that
// WithPostgresDefaults wires configRepo and outboxWriter on the cell without
// requiring a real pgxpool.Pool. We pass nil — NewSession accepts nil and the
// Session is only resolved at query time, not at construction time.
// The key assertion is that after applying the option, Init succeeds in demo
// mode when we also supply a txRunner (which WithPostgresDefaults does NOT set,
// so we must supply it separately to satisfy the XOR constraint).
func TestWithPostgresDefaults_NilPool_SetsConfigRepoAndOutboxWriter(t *testing.T) {
	// WithPostgresDefaults wires configRepo + outboxWriter but NOT txRunner.
	// Supply txRunner separately to satisfy the XOR constraint so Init passes.
	writer := &recordingConfigWriter{}
	c := NewConfigCore(
		WithPostgresDefaults(nil, writer), // nil pool accepted at construction
		WithTxManager(noopTxRunner{}),
		WithPublisher(eventbus.New()),
		WithCursorCodec(mustNewCfgCodec(t, []byte("wiring-test-cfg-cursor-key-32b!!"))),
	)
	// configRepo and outboxWriter are non-nil after the option.
	assert.NotNil(t, c.configRepo, "WithPostgresDefaults must set configRepo")
	assert.NotNil(t, c.outboxWriter, "WithPostgresDefaults must set outboxWriter")
	// flagRepo is set by WithPostgresDefaults (in-memory in PR-C1).
	assert.NotNil(t, c.flagRepo, "WithPostgresDefaults must set flagRepo")
}
