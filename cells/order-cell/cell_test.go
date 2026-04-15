package ordercell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/http/router"
)

func newTestDeps() cell.Dependencies {
	return cell.Dependencies{
		Config: make(map[string]any),
	}
}

// newTestCell creates an OrderCell with NoopWriter + NoopTxRunner (unified outbox path).
func newTestCell() *OrderCell {
	return NewOrderCell(
		WithRepository(mem.NewOrderRepository()),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
}

func TestOrderCell_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := newTestDeps()

	// Init
	require.NoError(t, c.Init(ctx, deps))
	assert.Len(t, c.OwnedSlices(), 2, "should have 2 slices (order-create, order-query)")

	// Start
	require.NoError(t, c.Start(ctx))
	assert.Equal(t, "healthy", c.Health().Status)
	assert.True(t, c.Ready())

	// Stop
	require.NoError(t, c.Stop(ctx))
	assert.Equal(t, "unhealthy", c.Health().Status)
	assert.False(t, c.Ready())
}

func TestOrderCell_Metadata(t *testing.T) {
	c := newTestCell()
	assert.Equal(t, "order-cell", c.ID())
	assert.Equal(t, cell.CellTypeCore, c.Type())
	assert.Equal(t, cell.L2, c.ConsistencyLevel())
}

func TestOrderCell_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, newTestDeps()))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestOrderCell_InitDefaults(t *testing.T) {
	tests := []struct {
		name       string
		opts       []Option
		wantSlices int
		wantErr    bool
	}{
		{
			name:    "no options fails — outboxWriter+txRunner required",
			opts:    nil,
			wantErr: true,
		},
		{
			name: "NoopWriter + NoopTxRunner succeeds (demo mode)",
			opts: []Option{
				WithOutboxWriter(outbox.NoopWriter{}),
				WithTxManager(persistence.NoopTxRunner{}),
			},
			wantSlices: 2,
		},
		{
			name: "with explicit repo + NoopWriter + NoopTxRunner",
			opts: []Option{
				WithRepository(mem.NewOrderRepository()),
				WithOutboxWriter(outbox.NoopWriter{}),
				WithTxManager(persistence.NoopTxRunner{}),
			},
			wantSlices: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewOrderCell(tt.opts...)
			err := c.Init(context.Background(), newTestDeps())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "outboxWriter and txRunner")
			} else {
				require.NoError(t, err)
				assert.Len(t, c.OwnedSlices(), tt.wantSlices)
			}
		})
	}
}

func TestOrderCell_DefaultInit_RequiresOutboxWriterAndTxRunner(t *testing.T) {
	c := NewOrderCell()
	err := c.Init(context.Background(), newTestDeps())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outboxWriter and txRunner")
	assert.Contains(t, err.Error(), "NoopWriter")
	assert.Contains(t, err.Error(), "NoopTxRunner")
}

func TestOrderCell_InitRejectsHalfConfiguredPath(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{
			name: "writer without tx manager",
			opts: []Option{WithOutboxWriter(outbox.NoopWriter{})},
		},
		{
			name: "tx manager without writer",
			opts: []Option{WithTxManager(persistence.NoopTxRunner{})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewOrderCell(tt.opts...)
			err := c.Init(context.Background(), newTestDeps())
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
		})
	}
}

func TestOrderCell_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewOrderCell(
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDurable,
	}
	err := c.Init(context.Background(), deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "durable mode rejects")
}

func TestOrderCell_DemoMode_AllowsNoopWriter(t *testing.T) {
	c := NewOrderCell(
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(context.Background(), deps))
}

func TestOrderCell_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, newTestDeps()))

	mux := &stubMux{}
	c.RegisterRoutes(mux)
	assert.GreaterOrEqual(t, mux.handleCount, 3, "should register at least 3 route patterns")
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

// --- Integration tests with real chi router ---

func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, newTestDeps()))

	r := router.New()
	c.RegisterRoutes(r)
	return r
}

func TestOrderCell_RouteCreateOrder(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"item":"test-widget"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"POST /api/v1/orders/ should return 201")
}

func TestOrderCell_RouteListOrders(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/orders/ should return 200")
}

func TestOrderCell_RouteGetOrder(t *testing.T) {
	r := initCellWithRouter(t)

	// Create an order first.
	body := `{"item":"queryable"}`
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	// Extract the ID from the create response.
	var createResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&createResp))
	orderID := createResp.Data.ID
	require.NotEmpty(t, orderID, "response should contain data.id")

	// GET the created order by its actual ID.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+orderID, nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/orders/{id} should return 200 for existing order")
}

func TestOrderCell_RouteGetOrder_NotFound(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/nonexistent", nil)
	r.ServeHTTP(rec, req)

	// 404 is the correct domain response for a nonexistent order.
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/orders/{id} should return 404 for nonexistent order")
}
