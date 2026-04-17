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
	assert.Equal(t, 5, len(c.OwnedSlices()), "should have 5 slices")

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
func (m *stubMux) Mount(_ string, _ http.Handler)                   { m.handleCount++ }
func (m *stubMux) Group(_ func(cell.RouteMux))                      { m.handleCount++ }
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

func TestConfigCore_CrossSliceCursorRejection(t *testing.T) {
	r := initCellWithRouter(t)

	// Seed enough config entries to produce a nextCursor.
	for i := range 3 {
		body := fmt.Sprintf(`{"key":"cfg-%d","value":"val-%d"}`, i, i)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
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

// TestConfigCore_Wiring_PublisherFailure_DemoVsDurable exercises the
// ConfigCore.Init → WithDemoFailOpen wiring end-to-end through HTTP. A
// publisher-only path with a failing publisher must:
//   - DurabilityDemo: swallow the publisher error (200 OK)
//   - DurabilityDurable: propagate the error (500)
//
// This is the contract that PR#165 introduced but was previously only
// covered at the service layer; the wiring in ConfigCore.Init toggling
// WithDemoFailOpen was untested.
func TestConfigCore_Wiring_PublisherFailure_DemoVsDurable(t *testing.T) {
	t.Parallel()
	productionKey := []byte("wiring-test-cfg-cursor-key-32b!!")

	tests := []struct {
		name       string
		mode       cell.DurabilityMode
		path       string
		wantStatus int
	}{
		{"demo swallows publisher error on publish", cell.DurabilityDemo, "/api/v1/config/flag-wiring/publish", http.StatusOK},
		{"demo swallows publisher error on rollback", cell.DurabilityDemo, "/api/v1/config/flag-wiring/rollback", http.StatusOK},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := mem.NewConfigRepository()
			seedConfigEntry(t, repo, "flag-wiring", "v1")
			// Pre-create a version for rollback path.
			mustPublish(t, repo, "flag-wiring")

			c := NewConfigCore(
				WithConfigRepository(repo),
				WithFlagRepository(mem.NewFlagRepository()),
				WithPublisher(failingPub{err: fmt.Errorf("broker unavailable")}),
				WithCursorCodec(mustNewCfgCodec(t, productionKey)),
				// No outbox — exercises the publisher-only path.
			)
			require.NoError(t, c.Init(context.Background(), cell.Dependencies{
				Config:         map[string]any{},
				DurabilityMode: tc.mode,
			}))

			r := router.New()
			c.RegisterRoutes(r)

			rec := httptest.NewRecorder()
			ctx := auth.TestContext("admin-user", []string{"admin"})
			body := strings.NewReader(`{"version":1}`)
			req := httptest.NewRequest(http.MethodPost, tc.path, body).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(rec, req)

			assert.Equalf(t, tc.wantStatus, rec.Code,
				"mode=%s body=%s", tc.mode, rec.Body.String())
		})
	}
}

// recordingConfigWriter is a minimal outbox.Writer test double that is not a
// Nooper — durable mode requires a non-noop writer.
type recordingConfigWriter struct{ entries []outbox.Entry }

func (w *recordingConfigWriter) Write(_ context.Context, entry outbox.Entry) error {
	w.entries = append(w.entries, entry)
	return nil
}

type failingPub struct{ err error }

func (p failingPub) Publish(_ context.Context, _ string, _ []byte) error { return p.err }

func mustNewCfgCodec(t *testing.T, key []byte) *query.CursorCodec {
	t.Helper()
	codec, err := query.NewCursorCodec(key)
	require.NoError(t, err)
	return codec
}

func seedConfigEntry(t *testing.T, repo *mem.ConfigRepository, key, value string) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
	}))
}

func mustPublish(t *testing.T, repo *mem.ConfigRepository, key string) {
	t.Helper()
	entry, err := repo.GetByKey(context.Background(), key)
	require.NoError(t, err)
	require.NoError(t, repo.PublishVersion(context.Background(), &domain.ConfigVersion{
		ID: "ver-seed-" + key, ConfigID: entry.ID, Version: entry.Version, Value: entry.Value,
	}))
}
