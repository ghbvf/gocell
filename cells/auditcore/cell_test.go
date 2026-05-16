package auditcore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

// durableTxRunner is a TxRunner that does NOT advertise Noop(); auditcore's
// durable-mode init check rejects the old persistence.NoopTxRunner and accepts
// this. Used by tests that exercise durable-mode behavior without spinning up a
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

// newTestProtocol constructs a ledger.Protocol for testing.
func newTestProtocol(t testing.TB) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(testHMACKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	return p
}

// newTestMemStore constructs a ledger.MemStore for testing.
func newTestMemStore(t testing.TB, p *ledger.Protocol) *ledger.MemStore {
	t.Helper()
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	return store
}

func newTestCell(t testing.TB) *AuditCore {
	t.Helper()
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	return NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(cell.DemoCellTxManager()),
		WithMetricsProvider(metrics.NopProvider{}),
	)
}

// newTestRecorder returns a RegistryRecorder for demo mode with an empty config.
func newTestRecorder() *cell.RegistryRecorder {
	return cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
}

func TestAuditCore_Lifecycle(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	recorder := newTestRecorder()

	// Init
	require.NoError(t, c.Init(ctx, recorder))
	// auditappendsession, auditappenduser, auditappendconfig, auditappendrole,
	// auditquery = 5 slices (auditverify removed in Wave 2 Batch D).
	// A-02 RED: current cell.go still constructs 6 slices (auditverify present).
	assert.Equal(t, 5, len(c.OwnedSlices()), "should have 5 slices after auditverify removal")

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
	c := newTestCell(t)
	assert.Equal(t, "auditcore", c.ID())
	assert.Equal(t, cellvocab.CellTypeCore, c.Type())
	assert.Equal(t, cellvocab.L2, c.ConsistencyLevel())
}

func TestAuditCore_Startup(t *testing.T) {
	c := newTestCell(t)
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestAuditCore_MissingLedgerProtocol(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerStore(store),
		// No WithLedgerProtocol.
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(cell.DemoCellTxManager()),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	ctx := context.Background()
	err := c.Init(ctx, cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err, "should fail without LedgerProtocol")
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestAuditCore_NilLedgerProtocol_SentinelRejected(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(nil), // bare nil — sentinel sticky
		WithLedgerStore(store),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(cell.DemoCellTxManager()),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err, "nil LedgerProtocol must be rejected")
}

func TestAuditCore_MissingLedgerStore(t *testing.T) {
	p := newTestProtocol(t)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		// No WithLedgerStore.
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(cell.DemoCellTxManager()),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo))
	require.Error(t, err, "should fail without LedgerStore")
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// --- L2 Hard Gate: durable-mode dependency checks ---

func TestInit_DemoMode_OutboxWithoutTx_Fails(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		// txRunner intentionally omitted
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	var ecErrTxPair1 *errcode.Error
	require.True(t, errors.As(err, &ecErrTxPair1))
	assert.Contains(t, ecErrTxPair1.Message+" "+ecErrTxPair1.InternalMessage, "outboxWriter and txRunner")
}

func TestInit_DemoMode_TxWithoutOutbox_Fails(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithTxManager(cell.DemoCellTxManager()),
		// outboxWriter intentionally omitted
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	var ecErrTxPair2 *errcode.Error
	require.True(t, errors.As(err, &ecErrTxPair2))
	assert.Contains(t, ecErrTxPair2.Message+" "+ecErrTxPair2.InternalMessage, "outboxWriter and txRunner")
}

func TestInit_DemoMode_NoPublisherNoOutbox_Fails(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	var ecErrSink *errcode.Error
	require.True(t, errors.As(err, &ecErrSink))
	assert.Contains(t, ecErrSink.Message+" "+ecErrSink.InternalMessage, "explicit event sink")
}

func TestInit_DurableMode_RejectsNoopWriter(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(cell.DemoCellTxManager()),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, ecErr.Message+" "+ecErr.InternalMessage, "durable mode")
}

func TestInit_DemoMode_WithPublisher_Succeeds(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithMetricsProvider(metrics.NopProvider{}),
		// No outboxWriter, no txRunner — demo mode with publisher.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.NoError(t, err, "demo mode with publisher should succeed")
}

func TestInit_DemoMode_ExplicitNoopOutboxPair_Succeeds(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(cell.DemoCellTxManager()),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.NoError(t, err)
}

// TestAuditInit_WithEmitter_DirectInjection mirrors the accesscore WithEmitter
// test: a pre-composed emitter skips cell.ResolveEmitter and the cell accepts
// the injection in demo mode.
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed client.
func TestAuditInit_WithEmitter_DirectInjection(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
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
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithEmitter(outbox.NewNoopEmitter()),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo))
	require.Error(t, err)
	var ecErrMutex *errcode.Error
	require.True(t, errors.As(err, &ecErrMutex))
	assert.Contains(t, ecErrMutex.Message+" "+ecErrMutex.InternalMessage, "mutually exclusive")
}

// TestAuditInit_WithEmitter_DurableRequiresDurableEmitter guards the
// durable-mode safety invariant: directly-injected non-durable emitter must
// be rejected in DurabilityDurable mode.
func TestAuditInit_WithEmitter_DurableRequiresDurableEmitter(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	cursorCodec, err := query.NewCursorCodec([]byte("audit-wrapper-durable-test-key!!"))
	require.NoError(t, err)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithCursorCodec(cursorCodec),
		WithEmitter(outbox.NewNoopEmitter()), // non-durable
		WithTxManager(cell.DemoCellTxManager()),
	)
	err = c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
	require.Error(t, err)
	var ecErrDurable *errcode.Error
	require.True(t, errors.As(err, &ecErrDurable))
	assert.Contains(t, ecErrDurable.Message+" "+ecErrDurable.InternalMessage, "durable")
}

func TestAuditCore_RouteGroups(t *testing.T) {
	c := newTestCell(t)
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
	c := newTestCell(t)
	ctx := context.Background()
	recorder := newTestRecorder()
	require.NoError(t, c.Init(ctx, recorder))

	snap := recorder.Snapshot()
	// All 13 topics registered across 4 sub-slices.
	expectedTopics := []string{
		// 4 config events
		"event.config.entry-deleted.v1",
		"event.config.entry-upserted.v1",
		"event.config.rollback.v1",
		"event.config.version-published.v1",
		// 2 role events
		"event.role.assigned.v1",
		"event.role.revoked.v1",
		// 2 session events
		"event.session.created.v1",
		"event.session.revoked.v1",
		// 5 user lifecycle events
		"event.user.created.v1",
		"event.user.deleted.v1",
		"event.user.locked.v1",
		"event.user.unlocked.v1",
		"event.user.updated.v1",
	}
	assert.Equal(t, len(expectedTopics), len(snap.Subscriptions),
		"auditcore registers exactly %d topic subscriptions", len(expectedTopics))

	topicSet := make(map[string]bool, len(snap.Subscriptions))
	for _, sub := range snap.Subscriptions {
		topicSet[sub.Spec.Topic] = true
	}
	for _, topic := range expectedTopics {
		assert.True(t, topicSet[topic], "auditcore must subscribe to %s", topic)
	}
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
	c := newTestCell(t)
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
// behavior: a durable assembly that forgets to inject a production cursor codec
// must not silently fall back to the public demo key baked into the source tree.
func TestInit_DurableMode_RejectsMissingCursorCodec(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(&recordingWriter{})), // non-Nooper; durable-gated CheckNotNoop passes
		WithTxManager(persistence.WrapForCell(durableTxRunner{})),         // non-Nooper; durable-gated CheckNotNoop passes
		// No WithCursorCodec — durable mode must refuse the demo fallback.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
	assert.Contains(t, err.Error(), "cursor codec")
}

// TestAuditCore_Wiring_StaleCursor_DemoVsDurable exercises DurabilityMode →
// cell.Init → service → ExecutePagedQuery with a garbage cursor.
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
			tx:         cell.DemoTxRunner{},
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newTestProtocol(t)
			store := newTestMemStore(t, p)
			c := NewAuditCore(
				WithClock(clock.Real()),
				WithLedgerProtocol(p),
				WithLedgerStore(store),
				WithOutboxDeps(nil, outbox.WrapWriterForCell(tc.outbox)),
				WithTxManager(persistence.WrapForCell(tc.tx)),
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
	c := newTestCell(t)
	recorder := newTestRecorder()
	require.NoError(t, c.Init(context.Background(), recorder))

	snap := recorder.Snapshot()
	const emitterKey = "outbox-failopen-rate.auditcore"
	require.Contains(t, snap.HealthCheckers, emitterKey, "DirectEmitter health checker must be aggregated")
	assert.NoError(t, snap.HealthCheckers[emitterKey](context.Background()), "fresh emitter should be healthy")
}

// deadlineProbeStore is a ledger.Store whose Verify asserts that the caller
// supplied a context with a deadline (strictTailVerifyOnStartup must wrap ctx
// with WithTimeout). It does NOT actually wait for the deadline to fire —
// it returns ctx.DeadlineExceeded immediately so the test runs in milliseconds
// rather than paying 30 s of wall-clock cost (slowgate cap 15 s).
type deadlineProbeStore struct {
	ledger.Store
	gotDeadline bool
	verifyCalls int
}

func newDeadlineProbeStore(t *testing.T, inner ledger.Store) *deadlineProbeStore {
	t.Helper()
	return &deadlineProbeStore{Store: inner}
}

func (b *deadlineProbeStore) Tail(ctx context.Context) (ledger.TailSnapshot, error) {
	// Return a non-empty tail so strictTailVerifyOnStartup proceeds to Verify.
	return ledger.TailSnapshot{SeqNo: 1, EntryCount: 1}, nil
}

func (b *deadlineProbeStore) Verify(ctx context.Context, from, to int64) (bool, int64, error) {
	b.verifyCalls++
	_, b.gotDeadline = ctx.Deadline()
	// Synthesize the same error a real timed-out store would return so the
	// caller error path is exercised end-to-end.
	return false, 0, context.DeadlineExceeded
}

// TestStrictTailVerifyOnStartup_TimeoutCapped asserts that strictTailVerifyOnStartup
// wraps the caller context with a deadline before calling ledger.Store.Verify,
// so a hung store cannot stall k8s readiness indefinitely.
//
// F-04: rather than block on the production 30 s timeout (slowgate cap 15 s),
// the probe store inspects ctx.Deadline() at call time and returns
// DeadlineExceeded immediately — testing the deadline-injection contract
// without paying wall-clock cost.
func TestStrictTailVerifyOnStartup_TimeoutCapped(t *testing.T) {
	p := newTestProtocol(t)
	inner := newTestMemStore(t, p)
	probe := newDeadlineProbeStore(t, inner)

	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(probe),
		WithOutboxDeps(outbox.WrapPublisherForCell(eventbus.New(eventbus.WithClock(clock.Real()))), nil),
		WithOutboxDeps(nil, outbox.WrapWriterForCell(outbox.NoopWriter{})),
		WithTxManager(cell.DemoCellTxManager()),
		WithMetricsProvider(metrics.NopProvider{}),
	)

	err := c.Init(context.Background(), newTestRecorder())
	require.Error(t, err, "Init should fail when Verify returns DeadlineExceeded")

	assert.Equal(t, 1, probe.verifyCalls,
		"strictTailVerifyOnStartup should call Verify exactly once")
	assert.True(t, probe.gotDeadline,
		"strictTailVerifyOnStartup must wrap ctx with a deadline before calling Verify; "+
			"caller-supplied context.Background has none")
}

// TestAuditCore_HealthCheckers_AuditLedgerReady verifies that Init registers
// the "audit_ledger_ready" probe via cell.RegisterRepoReadiness typed funnel
// (ledger.Store implements cell.RepoHealthProber). The probe is always present
// regardless of the emitter type — MemStore always returns nil.
func TestAuditCore_HealthCheckers_AuditLedgerReady(t *testing.T) {
	c := newTestCell(t)
	recorder := newTestRecorder()
	require.NoError(t, c.Init(context.Background(), recorder))

	snap := recorder.Snapshot()
	const probeKey = "audit_ledger_ready"
	require.Contains(t, snap.HealthCheckers, probeKey,
		"audit_ledger_ready probe must be registered via cell.RegisterRepoReadiness")
	require.NoError(t, snap.HealthCheckers[probeKey](context.Background()),
		"MemStore audit_ledger_ready must return nil (always ready)")
}

// TestAuditCore_HealthCheckers_NilEmitter verifies that when the emitter does
// not implement the health-checker interface, no emitter health checkers are
// registered — but audit_ledger_ready is always present (ledger.Store satisfies
// cell.RepoHealthProber regardless of emitter type).
func TestAuditCore_HealthCheckers_NilEmitter(t *testing.T) {
	p := newTestProtocol(t)
	store := newTestMemStore(t, p)
	c := NewAuditCore(
		WithClock(clock.Real()),
		WithLedgerProtocol(p),
		WithLedgerStore(store),
		WithEmitter(outbox.NewNoopEmitter()), // WriterEmitter — no HealthCheckers method
	)
	recorder := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), recorder))
	snap := recorder.Snapshot()
	// WriterEmitter does not add emitter probes; only audit_ledger_ready is present.
	assert.NotContains(t, snap.HealthCheckers, "outbox-failopen-rate.auditcore",
		"WriterEmitter must not add outbox-failopen-rate checker")
	assert.Contains(t, snap.HealthCheckers, "audit_ledger_ready",
		"audit_ledger_ready must always be registered via cell.RegisterRepoReadiness")
}
