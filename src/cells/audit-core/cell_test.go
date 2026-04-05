package auditcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

// noopWriter is a no-op outbox.Writer for testing.
type noopWriter struct{}

func (noopWriter) Write(_ context.Context, _ outbox.Entry) error { return nil }

func newTestCell() *AuditCore {
	return NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithHMACKey(testHMACKey),
		WithOutboxWriter(noopWriter{}),
	)
}

func TestAuditCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells:     make(map[string]cell.Cell),
		Contracts: make(map[string]cell.Contract),
		Config:    make(map[string]any),
	}

	// Init
	require.NoError(t, c.Init(ctx, deps))
	assert.Equal(t, 4, len(c.OwnedSlices()), "should have 4 slices")

	// Start
	require.NoError(t, c.Start(ctx))
	assert.Equal(t, "healthy", c.Health().Status)
	assert.True(t, c.Ready())

	// Stop
	require.NoError(t, c.Stop(ctx))
	assert.Equal(t, "unhealthy", c.Health().Status)
	assert.False(t, c.Ready())
}

func TestAuditCore_Metadata(t *testing.T) {
	c := newTestCell()
	assert.Equal(t, "audit-core", c.ID())
	assert.Equal(t, cell.CellTypeCore, c.Type())
	assert.Equal(t, cell.L3, c.ConsistencyLevel())
}

func TestAuditCore_MissingHMACKey(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		// No HMAC key.
	)
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}

	err := c.Init(ctx, deps)
	assert.Error(t, err, "should fail without HMAC key")
}

func TestAuditCore_HMACKeyFromConfig(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithOutboxWriter(noopWriter{}),
	)
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: map[string]any{"audit.hmac_key": "config-provided-key-32bytes!!!!!"},
	}

	require.NoError(t, c.Init(ctx, deps))
}

func TestAuditCore_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	mux := &stubMux{}
	c.RegisterRoutes(mux)
	assert.GreaterOrEqual(t, mux.handleCount, 1, "should register at least 1 route pattern")
}

func TestAuditCore_RegisterSubscriptions(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	eb := eventbus.New()
	c.RegisterSubscriptions(eb)
	_ = eb.Close()
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
func (m *stubMux) Mount(_ string, _ http.Handler)  { m.handleCount++ }
func (m *stubMux) Group(_ func(cell.RouteMux))     { m.handleCount++ }

func TestAuditCore_RouteQueryEntries(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	r := router.New()
	c.RegisterRoutes(r)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/audit/entries should not return 404 (got %d)", rec.Code)
}
