package ordercell

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/router"
)

func newTestRec() *cell.RegistryRecorder {
	return cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
}

// demoTxRunner is a pass-through TxRunner for demo-mode tests. Replaces the
// deleted persistence.NoopTxRunner — no transactional isolation, suitable only
// for in-memory test doubles.
type demoTxRunner struct{}

func (demoTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = demoTxRunner{}

// newTestCell creates an OrderCell with NoopWriter + demoTxRunner (unified outbox path).
func newTestCell() *OrderCell {
	return NewOrderCell(
		WithRepository(mem.NewOrderRepository()),
		WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(demoTxRunner{})),
	)
}

func TestOrderCell_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()

	// Init
	require.NoError(t, c.Init(ctx, rec))
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
	assert.Equal(t, "ordercell", c.ID())
	assert.Equal(t, cellvocab.CellTypeCore, c.Type())
	assert.Equal(t, cellvocab.L2, c.ConsistencyLevel())
}

func TestOrderCell_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	require.NoError(t, c.Init(ctx, newTestRec()))
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
			name:    "no options fails without explicit outbox pair",
			opts:    nil,
			wantErr: true,
		},
		{
			name: "NoopWriter + NoopTxRunner succeeds (demo mode)",
			opts: []Option{
				WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
				WithTxManager(persistence.WrapForCell(demoTxRunner{})),
			},
			wantSlices: 2,
		},
		{
			name: "with explicit repo + NoopWriter + NoopTxRunner",
			opts: []Option{
				WithRepository(mem.NewOrderRepository()),
				WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
				WithTxManager(persistence.WrapForCell(demoTxRunner{})),
			},
			wantSlices: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewOrderCell(tt.opts...)
			err := c.Init(context.Background(), newTestRec())
			if tt.wantErr {
				require.Error(t, err)
				var ecErrHalf *errcode.Error
				require.True(t, errors.As(err, &ecErrHalf))
				assert.Contains(t, ecErrHalf.Message+" "+ecErrHalf.InternalMessage, "outboxWriter+txRunner")
				return
			}
			require.NoError(t, err)
			assert.Len(t, c.OwnedSlices(), tt.wantSlices)
		})
	}
}

func TestOrderCell_DefaultInit_DemoModeRequiresExplicitOutboxPair(t *testing.T) {
	c := NewOrderCell()
	err := c.Init(context.Background(), newTestRec())
	require.Error(t, err)
	var ecErrDefault *errcode.Error
	require.True(t, errors.As(err, &ecErrDefault))
	assert.Contains(t, ecErrDefault.Message+" "+ecErrDefault.InternalMessage, "outboxWriter+txRunner")
}

// TestOrderCell_DemoMode_RejectsHalfConfiguredPath verifies that exactly one
// of (outboxWriter, txRunner) being set is rejected at Init() time.
// Both sub-cases hit cell.ResolveCellEmitter::resolveDemoEmitter pairing
// invariant (writer XOR txRunner = error).
func TestOrderCell_DemoMode_RejectsHalfConfiguredPath(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{
			name: "writer present, txRunner absent → demo pairing invariant",
			opts: []Option{WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{}))},
		},
		{
			name: "txRunner present, writer absent → demo pairing invariant",
			opts: []Option{WithTxManager(persistence.WrapForCell(demoTxRunner{}))},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewOrderCell(tt.opts...)
			err := c.Init(context.Background(), newTestRec())
			require.Error(t, err)
			var ecErrReject *errcode.Error
			require.True(t, errors.As(err, &ecErrReject))
			assert.Contains(t, ecErrReject.Message+" "+ecErrReject.InternalMessage, "outboxWriter and txRunner")
		})
	}
}

// TestOrderCell_DurableModeRejected verifies that Init fails fast when the
// assembly runtime mode (DurabilityDurable) does not match the cell.yaml
// declaration (durabilityMode: demo). Examples cells are demo-only; this
// alignment check is enforced by BaseCell.Init.
func TestOrderCell_DurableModeRejected(t *testing.T) {
	c := NewOrderCell(
		WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(demoTxRunner{})),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
	declared, ok := ecErr.FindAttr("declared")
	require.True(t, ok, "expected 'declared' detail attr")
	assert.Equal(t, "demo", declared.Value.String())
	runtimeAttr, ok := ecErr.FindAttr("runtime")
	require.True(t, ok, "expected 'runtime' detail attr")
	assert.Equal(t, "durable", runtimeAttr.Value.String())
}

func TestOrderCell_DemoMode_AllowsNoopWriter(t *testing.T) {
	c := NewOrderCell(
		WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(demoTxRunner{})),
	)
	require.NoError(t, c.Init(context.Background(), newTestRec()))
}

func TestOrderCell_RouteGroups(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()
	require.NoError(t, c.Init(ctx, rec))
	snap := rec.Snapshot()

	mux := &stubMux{}
	for _, rg := range snap.RouteGroups {
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				mux.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			} else {
				require.NoError(t, rg.Register(mux))
			}
		}
	}
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
	rec := newTestRec()
	require.NoError(t, c.Init(ctx, rec))
	snap := rec.Snapshot()

	r := router.MustNew(router.WithRouterClock(clock.Real()))
	for _, rg := range snap.RouteGroups {
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			} else {
				require.NoError(t, rg.Register(r))
			}
		}
	}
	require.NoError(t, r.FinalizeAuth())
	return r
}

func TestOrderCell_RouteCreateOrder(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"item":"test-widget"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(body))
	req = req.WithContext(auth.TestContext("usr-1", []string{dto.RoleCustomer}))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"POST /api/v1/orders/ should return 201")
}

func TestJOrdercreateHttpCreate(t *testing.T) {
	TestOrderCell_RouteCreateOrder(t)
}

func TestOrderCell_RouteListOrders(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/", nil)
	req = req.WithContext(auth.TestContext("usr-1", []string{dto.RoleCustomer}))
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
	createReq = createReq.WithContext(auth.TestContext("usr-1", []string{dto.RoleCustomer}))
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
	req = req.WithContext(auth.TestContext("usr-1", []string{dto.RoleCustomer}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/orders/{id} should return 200 for existing order")
}

func TestOrderCell_RouteGetOrder_NotFound(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/nonexistent", nil)
	req = req.WithContext(auth.TestContext("usr-1", []string{dto.RoleCustomer}))
	r.ServeHTTP(rec, req)

	// 404 is the correct domain response for a nonexistent order.
	errcodetest.AssertWireCode(t, rec, http.StatusNotFound, errcode.ErrOrderNotFound)
}

// TestOrderCell_Authz_RejectsUnauthenticatedAndWrongRole verifies that the
// three protected routes (POST /orders, GET /orders, GET /orders/{id}) reject
// requests with no auth context (→ 401) and with an incorrect role (→ 403).
// This test acts as a regression guard: if the policy is accidentally changed
// to Public, all positive-path tests still pass but these cases will fail.
func TestOrderCell_Authz_RejectsUnauthenticatedAndWrongRole(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"item":"test-widget"}`

	tests := []struct {
		name       string
		method     string
		path       string
		ctx        context.Context
		wantStatus int
	}{
		{
			name:       "create no context → 401",
			method:     http.MethodPost,
			path:       "/api/v1/orders/",
			ctx:        context.Background(),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "create wrong role → 403",
			method:     http.MethodPost,
			path:       "/api/v1/orders/",
			ctx:        auth.TestContext("u-1", []string{"viewer"}),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "list no context → 401",
			method:     http.MethodGet,
			path:       "/api/v1/orders/",
			ctx:        context.Background(),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "list wrong role → 403",
			method:     http.MethodGet,
			path:       "/api/v1/orders/",
			ctx:        auth.TestContext("u-1", []string{"viewer"}),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "get no context → 401",
			method:     http.MethodGet,
			path:       "/api/v1/orders/some-id",
			ctx:        context.Background(),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "get wrong role → 403",
			method:     http.MethodGet,
			path:       "/api/v1/orders/some-id",
			ctx:        auth.TestContext("u-1", []string{"viewer"}),
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var req *http.Request
			if tt.method == http.MethodPost {
				req = httptest.NewRequest(tt.method, tt.path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			req = req.WithContext(tt.ctx)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code, "route %s %s", tt.method, tt.path)
		})
	}
}
