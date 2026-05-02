package auditcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

// durableTxRunner is a TxRunner that does NOT advertise Noop(); auditcore's
// durable-mode init check rejects persistence.NoopTxRunner and accepts this.
// Used by tests that exercise durable-mode behavior without spinning up a
// real database.
type durableTxRunner struct{}

func (durableTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = durableTxRunner{}

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
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithHMACKey(testHMACKey),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
}

// newTestRecorder returns a RegistryRecorder for demo mode with an empty config.
func newTestRecorder() *cell.RegistryRecorder {
	return cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
}

func TestAuditCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()

	// Init
	require.NoError(t, c.Init(ctx, recorder))
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
	assert.Equal(t, "auditcore", c.ID())
	assert.Equal(t, cell.CellTypeCore, c.Type())
	assert.Equal(t, cell.L2, c.ConsistencyLevel())
}

func TestAuditCore_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestAuditCore_MissingHMACKey(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		// No HMAC key.
	)
	ctx := context.Background()
	err := c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	assert.Error(t, err, "should fail without HMAC key")
}

func TestAuditCore_HMACKeyFromConfig(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	ctx := context.Background()
	err := c.Init(ctx, cell.NewRegistryRecorder(
		map[string]any{"audit.hmac_key": "config-provided-key-32bytes!!!!!"},
		cell.DurabilityDemo,
	))
	require.NoError(t, err)
}

// --- L2 Hard Gate: durable-mode dependency checks ---

func TestInit_DemoMode_OutboxWithoutTx_Fails(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithHMACKey(testHMACKey),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		// txRunner intentionally omitted
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outboxWriter and txRunner")
}

func TestInit_DemoMode_TxWithoutOutbox_Fails(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithHMACKey(testHMACKey),
		WithTxManager(persistence.NoopTxRunner{}),
		// outboxWriter intentionally omitted
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outboxWriter and txRunner")
}

func TestInit_DemoMode_NoPublisherNoOutbox_Fails(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicit event sink")
}

func TestInit_DurableMode_RejectsNoopWriter(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
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
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithHMACKey(testHMACKey),
		WithMetricsProvider(metrics.NopProvider{}),
		// No outboxWriter, no txRunner — demo mode with publisher.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.NoError(t, err, "demo mode with publisher should succeed")
}

func TestInit_DemoMode_ExplicitNoopOutboxPair_Succeeds(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		WithOutboxDeps(nil, outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.NoError(t, err)
}

// TestAuditInit_WithEmitter_DirectInjection mirrors the accesscore WithEmitter
// test: a pre-composed emitter skips cell.ResolveEmitter and the cell accepts
// the injection in demo mode.
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed client.
func TestAuditInit_WithEmitter_DirectInjection(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		WithEmitter(outbox.NewNoopEmitter()),
	)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo)))
	assert.NotNil(t, c.emitter)
	assert.Nil(t, c.pendingOutboxPub)
	assert.Nil(t, c.pendingOutboxWriter)
}

// TestAuditInit_WithEmitterAndOutboxDeps_MutuallyExclusive guards against
// setting both provisioning paths at once.
func TestAuditInit_WithEmitterAndOutboxDeps_MutuallyExclusive(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		WithEmitter(outbox.NewNoopEmitter()),
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestAuditInit_WithEmitter_DurableRequiresDurableEmitter guards the
// durable-mode safety invariant: directly-injected non-durable emitter must
// be rejected in DurabilityDurable mode.
func TestAuditInit_WithEmitter_DurableRequiresDurableEmitter(t *testing.T) {
	cursorCodec, err := query.NewCursorCodec([]byte("audit-wrapper-durable-test-key!!"))
	require.NoError(t, err)
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		WithCursorCodec(cursorCodec),
		WithEmitter(outbox.NewNoopEmitter()), // non-durable
		WithTxManager(persistence.NoopTxRunner{}),
	)
	err = c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "durable")
}

func TestAuditCore_RouteGroups(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	require.Len(t, snap.RouteGroups, 1, "auditcore should declare 1 route group")
	assert.Equal(t, cell.PrimaryListener, snap.RouteGroups[0].Listener)
	assert.Equal(t, "/api/v1/audit", snap.RouteGroups[0].Prefix)
	assert.NotNil(t, snap.RouteGroups[0].Register)

	mux := &stubMux{}
	require.NoError(t, snap.RouteGroups[0].Register(mux))
	assert.GreaterOrEqual(t, mux.handleCount, 1, "should register at least 1 route pattern")
}

func TestAuditCore_RegisterSubscriptions(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	assert.Equal(t, 13, len(snap.Subscriptions),
		"auditcore registers 13 topic handlers (5 user lifecycle + 2 session + 4 config + 2 role events)")

	topicSet := make(map[string]bool, len(snap.Subscriptions))
	for _, sub := range snap.Subscriptions {
		topicSet[sub.Spec.Topic] = true
	}
	assert.True(t, topicSet["event.user.updated.v1"])
	assert.True(t, topicSet["event.user.deleted.v1"])
	assert.True(t, topicSet["event.user.unlocked.v1"])
	assert.True(t, topicSet["event.config.entry-upserted.v1"])
	assert.True(t, topicSet["event.config.entry-deleted.v1"])
	assert.True(t, topicSet["event.config.version-published.v1"])
	assert.True(t, topicSet["event.config.rollback.v1"])
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
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
	for _, rg := range snap.RouteGroups {
		rg := rg
		r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
	}
	require.NoError(t, r.FinalizeAuth())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(auth.TestContext("usr-1", nil))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"GET /api/v1/audit/entries should return 200 (got %d)", rec.Code)
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
		WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
		WithHMACKey(testHMACKey),
		WithOutboxDeps(nil, &recordingWriter{}), // non-Nooper; durable-gated CheckNotNoop passes
		WithTxManager(durableTxRunner{}),        // non-Nooper; durable-gated CheckNotNoop passes
		// No WithCursorCodec — durable mode must refuse the demo fallback.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
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
			tx:         durableTxRunner{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "demo returns first page",
			mode:       cell.DurabilityDemo,
			outbox:     outbox.NoopWriter{},
			tx:         persistence.NoopTxRunner{},
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewAuditCore(
				WithAuditRepository(mem.NewAuditRepository()),
				WithArchiveStore(mem.NewArchiveStore()),
				WithOutboxDeps(eventbus.New(eventbus.WithClock(clock.Real())), nil),
				WithHMACKey(testHMACKey),
				WithOutboxDeps(nil, tc.outbox),
				WithTxManager(tc.tx),
				WithCursorCodec(mustNewCodec(t, productionKey)),
				WithMetricsProvider(metrics.NopProvider{}),
			)
			recorder := cell.NewRegistryRecorder(map[string]any{}, tc.mode)
			require.NoError(t, c.Init(context.Background(), recorder))
			snap := recorder.Snapshot()

			r := router.MustNew(router.WithRouterClock(clock.Real()))
			for _, rg := range snap.RouteGroups {
				rg := rg
				r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			}
			require.NoError(t, r.FinalizeAuth())

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

// TestAuditCore_HealthCheckers_WithDirectEmitter verifies that after Init with
// a DirectEmitter-backed publisher, the registry snapshot contains the
// outbox-failopen-rate checker scoped to "auditcore".
func TestAuditCore_HealthCheckers_WithDirectEmitter(t *testing.T) {
	c := newTestCell()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(context.Background(), recorder))

	snap := recorder.Snapshot()
	const emitterKey = "outbox-failopen-rate.auditcore"
	require.Contains(t, snap.HealthCheckers, emitterKey, "DirectEmitter health checker must be aggregated")
	assert.NoError(t, snap.HealthCheckers[emitterKey](context.Background()), "fresh emitter should be healthy")
}

// TestAuditCore_HealthCheckers_NilEmitter verifies that when the emitter does
// not implement the health-checker interface, no health checkers are registered.
func TestAuditCore_HealthCheckers_NilEmitter(t *testing.T) {
	c := NewAuditCore(
		WithAuditRepository(mem.NewAuditRepository()),
		WithArchiveStore(mem.NewArchiveStore()),
		WithHMACKey(testHMACKey),
		WithEmitter(outbox.NewNoopEmitter()), // WriterEmitter — no HealthCheckers method
	)
	recorder := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), recorder))
	snap := recorder.Snapshot()
	assert.Empty(t, snap.HealthCheckers, "WriterEmitter must produce empty health checkers map")
}
