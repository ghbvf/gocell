package auditcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestCell() *AuditCore {
	return NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithHMACKey(testHMACKey),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
	)
}

func TestAuditCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config: make(map[string]any),
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
	assert.Equal(t, cell.L2, c.ConsistencyLevel())
}

func TestAuditCore_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
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
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
	)
	ctx := context.Background()
	deps := cell.Dependencies{
		Config: map[string]any{"audit.hmac_key": "config-provided-key-32bytes!!!!!"},
	}

	require.NoError(t, c.Init(ctx, deps))
}

// --- L2 Hard Gate: XOR constraint + publisher check ---

func TestInit_TxRunnerXOR_OutboxWithoutTx(t *testing.T) {
	// outboxWriter present but txRunner missing → XOR mismatch → error
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithHMACKey(testHMACKey),
		WithOutboxWriter(outbox.NoopWriter{}),
		// txRunner intentionally omitted
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "both outboxWriter and txRunner")
}

func TestInit_TxRunnerXOR_TxWithoutOutbox(t *testing.T) {
	// txRunner present but outboxWriter missing → XOR mismatch → error
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithHMACKey(testHMACKey),
		WithTxManager(noopTxRunner{}),
		// outboxWriter intentionally omitted
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "both outboxWriter and txRunner")
}

func TestInit_DemoMode_RequiresPublisher(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		// No outboxWriter, no txRunner, no publisher.
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "publisher")
}

func TestInit_DemoMode_WithPublisher_Succeeds(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithHMACKey(testHMACKey),
		// No outboxWriter, no txRunner — demo mode with publisher.
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}})
	require.NoError(t, err, "demo mode with publisher should succeed")
}

func TestAuditCore_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
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
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	r := &celltest.StubEventRouter{}
	require.NoError(t, c.RegisterSubscriptions(r))
	assert.Equal(t, 6, r.HandlerCount(), "audit-core should register 6 topic handlers")
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

func TestAuditCore_RouteQueryEntries(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
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
