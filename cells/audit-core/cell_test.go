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

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

// mustNewCodec constructs a CursorCodec with the given key or fails the test.
func mustNewCodec(t *testing.T, key []byte) *query.CursorCodec {
	t.Helper()
	codec, err := query.NewCursorCodec(key)
	require.NoError(t, err)
	return codec
}

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
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
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
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
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
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
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
		Config:         map[string]any{"audit.hmac_key": "config-provided-key-32bytes!!!!!"},
		DurabilityMode: cell.DurabilityDemo,
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
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}, DurabilityMode: cell.DurabilityDemo})
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
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}, DurabilityMode: cell.DurabilityDemo})
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
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}, DurabilityMode: cell.DurabilityDemo})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "publisher")
}

func TestInit_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	deps := cell.Dependencies{
		Config:         map[string]any{},
		DurabilityMode: cell.DurabilityDurable,
	}
	err := c.Init(context.Background(), deps)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "durable mode")
}

func TestInit_DemoMode_WithPublisher_Succeeds(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithHMACKey(testHMACKey),
		// No outboxWriter, no txRunner — demo mode with publisher.
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: map[string]any{}, DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, err, "demo mode with publisher should succeed")
}

func TestAuditCore_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
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
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))

	r := &celltest.StubEventRouter{}
	require.NoError(t, c.RegisterSubscriptions(r))
	assert.Equal(t, 8, r.HandlerCount(),
		"audit-core should register 8 topic handlers (6 legacy + 2 role events)")
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

func TestAuditCore_RouteQueryEntries(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
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

// TestInit_DurableMode_RejectsMissingCursorCodec locks the fail-fast
// behavior introduced with RunMode wiring: a durable assembly that forgets
// to inject a production cursor codec must not silently fall back to the
// public demo key baked into the source tree. Paired with
// TestAuditCore_Wiring_StaleCursor_DemoVsDurable below which exercises the
// same wiring when a codec *is* provided.
func TestInit_DurableMode_RejectsMissingCursorCodec(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithPublisher(eventbus.New()),
		WithHMACKey(testHMACKey),
		WithOutboxWriter(&recordingWriter{}), // non-Nooper; durable-gated CheckNotNoop passes
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

// TestAuditCore_Wiring_StaleCursor_DemoVsDurable is a wiring-level
// regression: it exercises DurabilityMode → cell.Init → service →
// ExecutePagedQuery with a garbage cursor and asserts that demo silently
// returns the first page while durable returns ErrCursorInvalid. This
// branch was previously only covered at the pkg/query helper level.
// An admin identity is injected via auth.TestContext because the
// audit-query handler calls auth.RequireSelfOrRole.
func TestAuditCore_Wiring_StaleCursor_DemoVsDurable(t *testing.T) {
	t.Parallel()
	productionKey := []byte("wiring-test-audit-cursor-key-32b")

	tests := []struct {
		name       string
		mode       cell.DurabilityMode
		outbox     outbox.Writer
		tx         persistence.TxRunner
		wantStatus int
	}{
		{
			name:       "durable refuses stale cursor",
			mode:       cell.DurabilityDurable,
			outbox:     &recordingWriter{},
			tx:         noopTxRunner{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "demo returns first page",
			mode:       cell.DurabilityDemo,
			outbox:     outbox.NoopWriter{},
			tx:         noopTxRunner{},
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewAuditCore(
				WithAuditRepository(mem.NewAuditRepository()),
				WithArchiveStore(mem.NewArchiveStore()),
				WithPublisher(eventbus.New()),
				WithHMACKey(testHMACKey),
				WithOutboxWriter(tc.outbox),
				WithTxManager(tc.tx),
				WithCursorCodec(mustNewCodec(t, productionKey)),
			)
			require.NoError(t, c.Init(context.Background(), cell.Dependencies{
				Config:         map[string]any{},
				DurabilityMode: tc.mode,
			}))

			r := router.New()
			c.RegisterRoutes(r)

			rec := httptest.NewRecorder()
			ctx := auth.TestContext("admin-user", []string{"admin"})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries?cursor=garbage-token", nil).WithContext(ctx)
			r.ServeHTTP(rec, req)

			assert.Equalf(t, tc.wantStatus, rec.Code,
				"unexpected status for mode=%s body=%s", tc.mode, rec.Body.String())
		})
	}
}

// recordingWriter is a minimal outbox.Writer test double that is not a
// Nooper — durable mode requires a non-noop writer.
type recordingWriter struct{ entries []outbox.Entry }

func (w *recordingWriter) Write(_ context.Context, entry outbox.Entry) error {
	w.entries = append(w.entries, entry)
	return nil
}
