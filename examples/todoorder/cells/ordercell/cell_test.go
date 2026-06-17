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
	orderprojection "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/orderprojection"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/http/router"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	orderstatuschanged "github.com/ghbvf/gocell/generated/contracts/event/order-status-changed/v1"
)

func newTestRec() *cell.RegistryRecorder {
	return cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
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
	return NewOrderCell(clock.Real(),
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
	assert.Len(t, c.OwnedSlices(), 4, "expected 4 owned slices")

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
	assert.Equal(t, cellvocab.L3, c.ConsistencyLevel())
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
			wantSlices: 4,
		},
		{
			name: "with explicit repo + NoopWriter + NoopTxRunner",
			opts: []Option{
				WithRepository(mem.NewOrderRepository()),
				WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
				WithTxManager(persistence.WrapForCell(demoTxRunner{})),
			},
			wantSlices: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewOrderCell(clock.Real(), tt.opts...)
			err := c.Init(context.Background(), newTestRec())
			if tt.wantErr {
				require.Error(t, err)
				var ecErrHalf *errcode.Error
				require.True(t, errors.As(err, &ecErrHalf))
				assert.Contains(t, ecErrHalf.Message, "outboxWriter+txRunner")
				return
			}
			require.NoError(t, err)
			assert.Len(t, c.OwnedSlices(), tt.wantSlices)
		})
	}
}

func TestOrderCell_DefaultInit_DemoModeRequiresExplicitOutboxPair(t *testing.T) {
	c := NewOrderCell(clock.Real())
	err := c.Init(context.Background(), newTestRec())
	require.Error(t, err)
	var ecErrDefault *errcode.Error
	require.True(t, errors.As(err, &ecErrDefault))
	assert.Contains(t, ecErrDefault.Message, "outboxWriter+txRunner")
}

// TestOrderCell_DemoMode_RejectsHalfConfiguredPath verifies that exactly one
// of (outboxWriter, txRunner) being set is rejected at Init() time.
// Both sub-cases hit outbox.ResolveCellEmitter::resolveDemoEmitter pairing
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
			c := NewOrderCell(clock.Real(), tt.opts...)
			err := c.Init(context.Background(), newTestRec())
			require.Error(t, err)
			var ecErrReject *errcode.Error
			require.True(t, errors.As(err, &ecErrReject))
			assert.Contains(t, ecErrReject.Message, "outboxWriter and txRunner")
		})
	}
}

func TestOrderCell_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewOrderCell(clock.Real(),
		WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(persistence.WrapForCell(demoTxRunner{})),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable))
	require.Error(t, err)
	var ecErrNoopWriter *errcode.Error
	require.True(t, errors.As(err, &ecErrNoopWriter))
	assert.Contains(t, ecErrNoopWriter.Message, "durable mode")
}

// TestOrderCell_DurableMode_RejectsMissingCursorCodec locks the fail-fast
// behavior introduced with RunMode wiring: a durable assembly that forgets
// to inject a production cursor codec must not silently fall back to the
// public demo key baked into the source tree.
func TestOrderCell_DurableMode_RejectsMissingCursorCodec(t *testing.T) {
	c := NewOrderCell(clock.Real(),
		WithRepository(mem.NewOrderRepository()),
		WithOutboxWriter(outbox.WrapWriterForCell(&orderRecordingWriter{})),
		WithTxManager(persistence.WrapForCell(orderLocalTxRunner{})),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, outbox.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
	assert.Contains(t, err.Error(), "cursor codec")
}

// orderRecordingWriter is a non-Nooper outbox.Writer for durable-mode tests
// that need a legitimate writer to pass CheckNotNoop but don't exercise
// actual outbox flow.
type orderRecordingWriter struct{ entries []outbox.Entry }

func (w *orderRecordingWriter) Write(_ context.Context, e outbox.Entry) error {
	w.entries = append(w.entries, e)
	return nil
}

// orderLocalTxRunner is a non-Nooper persistence.TxRunner test double that
// simply invokes the fn directly. Durable-mode CheckNotNoop rejects
// persistence.NoopTxRunner but accepts any other type implementing the
// interface, so this exists to isolate the cursor-codec fail-fast test.
type orderLocalTxRunner struct{}

func (orderLocalTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func TestOrderCell_DemoMode_AllowsNoopWriter(t *testing.T) {
	c := NewOrderCell(clock.Real(),
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

// TestOrderCell_ProjectionRegistrations asserts that OrderCell.Init registers
// BOTH the order_status and order_transition projections through the production
// generated wiring (cell_gen.go RegisterProjection) — #1574 F4. The journey
// checkRef test (TestJOrderprojectionStatusTransitionProjection) drives the
// orderprojection.Service handlers directly, so a dropped order_transition
// registration in cell_gen.go would still pass it; this test reads the
// RegistrySnapshot so such a wiring regression reds.
func TestOrderCell_ProjectionRegistrations(t *testing.T) {
	c := newTestCell()
	rec := newTestRec()
	require.NoError(t, c.Init(context.Background(), rec))

	projs := rec.Snapshot().Projections
	require.Len(t, projs, 2, "ordercell must register exactly order_status + order_transition")

	byID := make(map[string]cell.ProjectionRequest, len(projs))
	for _, p := range projs {
		byID[p.ProjectionID] = p
	}

	status, ok := byID["order_status"]
	require.True(t, ok, "order_status projection must be registered")
	assert.Equal(t, "event.order-created.v1", status.Spec.ID)
	assert.Equal(t, "event.order-created.v1", status.Spec.Topic)
	assert.Equal(t, "ordercell", status.CellID)
	assert.Equal(t, "orderprojection", status.SliceID)
	assert.NotNil(t, status.OnReset, "order_status must carry its ResetOrderStatus hook")

	transition, ok := byID["order_transition"]
	require.True(t, ok, "order_transition projection must be registered (#1574 F4)")
	assert.Equal(t, "event.order-status-changed.v1", transition.Spec.ID)
	assert.Equal(t, "event.order-status-changed.v1", transition.Spec.Topic)
	assert.Equal(t, "ordercell", transition.CellID)
	assert.Equal(t, "orderprojection", transition.SliceID)
	assert.NotNil(t, transition.OnReset, "order_transition must carry its ResetOrderTransition hook")
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

func initCellWithRouter(t *testing.T) (*router.Router, *OrderCell) {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()
	require.NoError(t, c.Init(ctx, rec))
	snap := rec.Snapshot()

	r := mustNewRouter(t)
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
	return r, c
}

// withAuthorizer returns a copy of ctx with the cell's authorizer injected.
// Required for tests that exercise permission-gated routes: RequirePermission
// and RequirePermissionForResource both fail-closed (403) when no Authorizer
// is in context.
func withAuthorizer(ctx context.Context, c *OrderCell) context.Context {
	return auth.WithAuthorizer(ctx, c.Authorizer())
}

func TestOrderCell_RouteCreateOrder(t *testing.T) {
	r, c := initCellWithRouter(t)

	body := `{"item":"test-widget"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(body))
	req = req.WithContext(withAuthorizer(auth.TestContext("usr-1", []string{dto.RoleCustomer}), c))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"POST /api/v1/orders/ should return 201")
}

func TestJOrdercreateHttpCreate(t *testing.T) {
	TestOrderCell_RouteCreateOrder(t)
}

// TestOrderCell_RouteConfirmOrder drives the orderconfirm slice end-to-end over
// the router: create a pending order, then PATCH it to confirmed. This does not
// depend on event delivery (the projection is updated only when events are
// delivered, which demo-mode NoopWriter skips), so it is a valid auto journey
// criterion for the confirm command path.
func TestOrderCell_RouteConfirmOrder(t *testing.T) {
	r, c := initCellWithRouter(t)

	// Create a pending order first. The creating subject becomes order.Owner.
	const owner = "usr-1"
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(`{"item":"confirmable"}`))
	createReq = createReq.WithContext(withAuthorizer(auth.TestContext(owner, []string{dto.RoleCustomer}), c))
	createReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	var createResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&createResp))
	orderID := createResp.Data.ID
	require.NotEmpty(t, orderID, "response should contain data.id")

	// PATCH the order to confirmed. Must use the same subject as the creator
	// (owner) because confirm is owner-scoped (RequirePermissionForResource).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/orders/"+orderID+"/status", strings.NewReader(`{"status":"confirmed"}`))
	req = req.WithContext(withAuthorizer(auth.TestContext(owner, []string{dto.RoleCustomer}), c))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"PATCH /api/v1/orders/{id}/status should return 200 for owner of a pending order")
}

// TestJOrderprojectionHttpConfirm is the auto checkRef for J-orderprojection
// passCriteria journey.J-orderprojection.http-confirm (VERIFY-06).
func TestJOrderprojectionHttpConfirm(t *testing.T) {
	TestOrderCell_RouteConfirmOrder(t)
}

// TestJOrderprojectionStatusTransitionProjection is the auto checkRef for
// J-orderprojection passCriteria journey.J-orderprojection.status-transition-projection
// (VERIFY-06). It drives the order_transition projection's HandleOrderStatusChanged
// directly — deterministic, since demo-mode NoopWriter skips live event delivery —
// and asserts a created→confirmed transition moves the order between status
// buckets in the composed status-summary read model (#1482).
func TestJOrderprojectionStatusTransitionProjection(t *testing.T) {
	svc, err := orderprojection.NewService()
	require.NoError(t, err)
	ctx := context.Background()

	createdPayload, err := json.Marshal(ordercreated.Payload{ID: "order-X", Item: "widget", Status: "pending"})
	require.NoError(t, err)
	require.NoError(t, svc.HandleOrderCreated(ctx, outboxtest.NewEntry("event.order-created.v1", createdPayload)))
	require.Equal(t, "pending", projectionStatusOf(svc.Query(ctx), "order-X"),
		"order-X is pending after creation")

	changedPayload, err := json.Marshal(orderstatuschanged.Payload{ID: "order-X", OldStatus: "pending", NewStatus: "confirmed"})
	require.NoError(t, err)
	require.NoError(t, svc.HandleOrderStatusChanged(ctx, outboxtest.NewEntry("event.order-status-changed.v1", changedPayload)))
	assert.Equal(t, "confirmed", projectionStatusOf(svc.Query(ctx), "order-X"),
		"consuming order-status-changed.v1 must move the order to its new status bucket")
}

// projectionStatusOf returns the status bucket containing orderID in the
// composed summary, or "" if absent.
func projectionStatusOf(s orderprojection.Summary, orderID string) string {
	for _, b := range s.Statuses {
		for _, id := range b.OrderIDs {
			if id == orderID {
				return b.Status
			}
		}
	}
	return ""
}

func TestOrderCell_RouteListOrders(t *testing.T) {
	r, c := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/", nil)
	req = req.WithContext(withAuthorizer(auth.TestContext("usr-1", []string{dto.RoleCustomer}), c))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/orders/ should return 200")
}

func TestOrderCell_RouteGetOrder(t *testing.T) {
	r, c := initCellWithRouter(t)

	// Create an order first. The creating subject becomes order.Owner.
	const owner = "usr-1"
	body := `{"item":"queryable"}`
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/orders/", strings.NewReader(body))
	createReq = createReq.WithContext(withAuthorizer(auth.TestContext(owner, []string{dto.RoleCustomer}), c))
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

	// GET the created order by its actual ID — must use the same owner subject.
	// GET is owner-scoped: RequirePermissionForResource("id", PermOrderRead())
	// calls PDP.Authorize(subject, orderID, "order:read"), PDP fetches
	// order.Owner via repo.GetByID and allows when owner==subject.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+orderID, nil)
	req = req.WithContext(withAuthorizer(auth.TestContext(owner, []string{dto.RoleCustomer}), c))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/orders/{id} should return 200 for owner of the order")
}

func TestOrderCell_RouteGetOrder_NotFound(t *testing.T) {
	r, c := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/nonexistent", nil)
	req = req.WithContext(withAuthorizer(auth.TestContext("usr-1", []string{dto.RoleCustomer}), c))
	r.ServeHTTP(rec, req)

	// With owner-scoped gate (RequirePermissionForResource), the PDP is called
	// first. The PDP performs a PIP lookup (repo.GetByID("nonexistent")) which
	// returns not-found, so the PDP denies with fail-closed → 403.
	// The domain 404 is never reached (gate fires before the handler).
	errcodetest.AssertWireCode(t, rec, http.StatusForbidden, errcode.ErrAuthForbidden)
}

// TestOrderCell_Authz_RejectsUnauthenticatedAndWrongRole verifies that the
// protected routes reject requests with no auth context (→ 401) and with an
// incorrect role (→ 403). For owner-scoped routes (GET /orders/{id}), "wrong
// role" with an authorizer in context still reaches the PDP — and since the
// resource doesn't exist in this test, the PDP denies (fail-closed → 403).
// This test acts as a regression guard: if the policy is accidentally changed
// to Public, all positive-path tests still pass but these cases will fail.
func TestOrderCell_Authz_RejectsUnauthenticatedAndWrongRole(t *testing.T) {
	r, c := initCellWithRouter(t)

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
			ctx:        withAuthorizer(auth.TestContext("u-1", []string{"viewer"}), c),
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
			ctx:        withAuthorizer(auth.TestContext("u-1", []string{"viewer"}), c),
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
			name:   "get non-owner (resource not found) → 403",
			method: http.MethodGet,
			path:   "/api/v1/orders/some-id",
			// owner-scoped: PDP fetches order.Owner via repo; "some-id" does not
			// exist → PDP denies (fail-closed) → 403 regardless of role.
			ctx:        withAuthorizer(auth.TestContext("u-1", []string{dto.RoleCustomer}), c),
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

func mustNewRouter(t *testing.T) *router.Router {
	t.Helper()
	r, err := router.New(clock.Real())
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	return r
}
