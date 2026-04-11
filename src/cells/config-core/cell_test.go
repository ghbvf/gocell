package configcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopWriter is a no-op outbox.Writer for testing.
type noopWriter struct{}

func (noopWriter) Write(_ context.Context, _ outbox.Entry) error { return nil }

func newTestCell() *ConfigCore {
	return NewConfigCore(
		WithConfigRepository(mem.NewConfigRepository()),
		WithFlagRepository(mem.NewFlagRepository()),
		WithPublisher(eventbus.New()),
		WithOutboxWriter(noopWriter{}),
	)
}

func TestConfigCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells:     make(map[string]cell.Cell),
		Contracts: make(map[string]cell.Contract),
		Config:    make(map[string]any),
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
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestConfigCore_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
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
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	r := &stubEventRouter{}
	require.NoError(t, c.RegisterSubscriptions(r))
	assert.Equal(t, 1, r.count, "config-core should register 1 topic handler")
}

// stubEventRouter implements cell.EventRouter for testing.
type stubEventRouter struct {
	count int
}

func (r *stubEventRouter) AddHandler(_ string, _ outbox.EntryHandler) { r.count++ }

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
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
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
