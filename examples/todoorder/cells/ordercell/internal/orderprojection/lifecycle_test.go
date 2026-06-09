package orderprojection_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orderprojection "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/orderprojection"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	orderstatuschanged "github.com/ghbvf/gocell/generated/contracts/event/order-status-changed/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/wrapper"
	testtime "github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// topicOrderCreated / topicOrderStatusChanged mirror the production routing
// topics (= generated spec.Topic = producer WithTopic). The per-spec replay
// filter applies an entry only when its RoutingTopic() equals the subscribed
// spec.Topic, so the test entries and specs must agree on these values.
const (
	topicOrderCreated       = "event.order-created.v1"
	topicOrderStatusChanged = "event.order-status-changed.v1"
)

// nopLifecycleTracer is a no-op wrapper.Tracer for the lifecycle test.
type nopLifecycleTracer struct{}

func (nopLifecycleTracer) Start(ctx context.Context, _ string, _ ...wrapper.Attr) (context.Context, wrapper.Span) {
	return ctx, nopLifecycleSpan{}
}

type nopLifecycleSpan struct{}

func (nopLifecycleSpan) SetAttributes(_ ...wrapper.Attr)          {}
func (nopLifecycleSpan) RecordError(_ error)                      {}
func (nopLifecycleSpan) SetStatus(_ wrapper.StatusCode, _ string) {}
func (nopLifecycleSpan) End()                                     {}

// lifecycleDemoTxRunner is a pass-through TxRunner for the lifecycle test.
type lifecycleDemoTxRunner struct{}

func (lifecycleDemoTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	ctx, drainAfterCommit := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := fn(ctx); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark)
		return err
	}
	if drainAfterCommit {
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// lifecycleRegistrar is a minimal SubscribeRegistrar that captures the handler.
type lifecycleRegistrar struct {
	handler outbox.EntryHandler
}

func (r *lifecycleRegistrar) Subscribe(
	_ contractspec.ContractSpec,
	handler outbox.EntryHandler,
	_ string,
	_ string,
	_ ...cell.SubscriptionOption,
) error {
	r.handler = handler
	return nil
}

func eventSpec(id, topic string) contractspec.ContractSpec {
	return contractspec.ContractSpec{ID: id, Kind: "event", Transport: "amqp", Topic: topic}
}

type projectionCoordFixture struct {
	ProjectionID string
	Store        projection.CheckpointStore
	Replay       projection.ReplaySource
	Cursor       projection.Cursor
	Spec         contractspec.ContractSpec
	Apply        projection.Apply
	OnReset      projection.OnReset
}

// newProjectionCoord builds a Coordinator for one projection over the shared
// whole-journal replay source and returns it plus its registered live handler.
func newProjectionCoord(t *testing.T, f projectionCoordFixture) (*projection.Coordinator, outbox.EntryHandler) {
	t.Helper()
	reg := &lifecycleRegistrar{}
	coord, err := projection.NewCoordinator(clock.Real(), projection.CoordinatorConfig{
		Registrar:    reg,
		CellID:       "ordercell",
		ProjectionID: f.ProjectionID,
		TxRunner:     lifecycleDemoTxRunner{},
		Store:        f.Store,
		Cursor:       f.Cursor,
		Replay:       f.Replay,
		Tracer:       nopLifecycleTracer{},
	})
	require.NoError(t, err)
	require.NoError(t, coord.Subscribe(context.Background(), f.Spec, f.Apply, projection.WithOnReset(f.OnReset)))
	require.NotNil(t, reg.handler, "coordinator must register an event handler")
	return coord, reg.handler
}

func appendCreated(t *testing.T, src *projection.MemReplaySource, id, status string) outbox.Entry {
	t.Helper()
	b, err := json.Marshal(ordercreated.Payload{ID: id, Item: "widget", Status: status})
	require.NoError(t, err)
	e := outboxtest.NewEntry(topicOrderCreated, b)
	src.Append(e)
	return e
}

func appendStatusChanged(t *testing.T, src *projection.MemReplaySource, id, oldStatus, newStatus string) outbox.Entry {
	t.Helper()
	b, err := json.Marshal(orderstatuschanged.Payload{ID: id, OldStatus: oldStatus, NewStatus: newStatus})
	require.NoError(t, err)
	e := outboxtest.NewEntry(topicOrderStatusChanged, b)
	src.Append(e)
	return e
}

// currentStatusOf returns the composed current status of an order in the summary
// (the bucket containing it), or "" if absent.
func currentStatusOf(s orderprojection.Summary, orderID string) string {
	for _, b := range s.Statuses {
		for _, id := range b.OrderIDs {
			if id == orderID {
				return b.Status
			}
		}
	}
	return ""
}

// TestOrderProjection_FanInLifecycle verifies the #1482 multi-stream fan-in:
//  1. Two single-stream projections (order_status from order-created,
//     order_transition from order-status-changed) feed one composed read model.
//  2. A status transition moves an order between status buckets at query time.
//  3. Rebuilding ONE projection does not wipe the other's sub-view (disjoint
//     reset) — the transition survives a order_status rebuild, and the created
//     view survives a order_transition rebuild — thanks to the per-spec replay
//     filter skipping foreign streams while advancing the checkpoint.
func TestOrderProjection_FanInLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	checkpointStore := projection.NewMemCheckpointStore()
	replaySource := projection.NewMemReplaySource() // shared whole-journal source
	cursor, err := projection.NewMemCursor(replaySource)
	require.NoError(t, err)

	svc, err := orderprojection.NewService()
	require.NoError(t, err)

	createdCoord, createdHandler := newProjectionCoord(t, projectionCoordFixture{
		ProjectionID: "order_status",
		Store:        checkpointStore,
		Replay:       replaySource,
		Cursor:       cursor,
		Spec:         eventSpec(topicOrderCreated, topicOrderCreated),
		Apply:        svc.HandleOrderCreated,
		OnReset:      svc.ResetOrderStatus,
	})

	transitionCoord, transitionHandler := newProjectionCoord(t, projectionCoordFixture{
		ProjectionID: "order_transition",
		Store:        checkpointStore,
		Replay:       replaySource,
		Cursor:       cursor,
		Spec:         eventSpec(topicOrderStatusChanged, topicOrderStatusChanged),
		Apply:        svc.HandleOrderStatusChanged,
		OnReset:      svc.ResetOrderTransition,
	})

	// -- Phase 1: cold-start live delivery (both streams) --
	eCreatedA := appendCreated(t, replaySource, "order-A", "pending")
	eCreatedB := appendCreated(t, replaySource, "order-B", "pending")
	assert.Equal(t, outbox.DispositionAck, createdHandler(ctx, eCreatedA).Disposition)
	assert.Equal(t, outbox.DispositionAck, createdHandler(ctx, eCreatedB).Disposition)

	// order-A transitions pending → confirmed; order-B stays pending.
	eConfirmA := appendStatusChanged(t, replaySource, "order-A", "pending", "confirmed")
	assert.Equal(t, outbox.DispositionAck, transitionHandler(ctx, eConfirmA).Disposition)

	before := svc.Query(ctx)
	assert.Equal(t, int64(2), before.TotalOrders, "two orders total")
	assert.Equal(t, "confirmed", currentStatusOf(before, "order-A"), "order-A reflects the status transition")
	assert.Equal(t, "pending", currentStatusOf(before, "order-B"), "order-B stays pending")

	// -- Exactly-once: re-deliver the transition; latest map is idempotent --
	assert.Equal(t, outbox.DispositionAck, transitionHandler(ctx, eConfirmA).Disposition, "re-deliver acks")
	assert.Equal(t, before.TotalOrders, svc.Query(ctx).TotalOrders, "re-deliver does not change totals")

	// -- Bad payloads → permanent reject on both handlers --
	assert.Equal(t, outbox.DispositionReject,
		createdHandler(ctx, outboxtest.NewEntry(topicOrderCreated, []byte("not-json"))).Disposition)
	assert.Equal(t, outbox.DispositionReject,
		transitionHandler(ctx, outboxtest.NewEntry(topicOrderStatusChanged, []byte("not-json"))).Disposition)

	// -- Phase 2: rebuild ONLY order_status. Its onReset clears the created
	// sub-view; replay over the shared whole-journal source re-applies the two
	// order-created entries and SKIPS the foreign order-status-changed entry
	// (per-spec filter). The transition sub-view (latest) is untouched, so the
	// composed query must still show order-A as confirmed. --
	require.NoError(t, createdCoord.Rebuild(ctx))
	testwait.External(t, "orderprojection-rebuild-order-status",
		func() bool { return createdCoord.Phase() == projection.PhaseLive },
		testtime.EventuallyLong, testtime.FastPoll, "phase != PhaseLive")

	after := svc.Query(ctx)
	assert.Equal(t, before.TotalOrders, after.TotalOrders,
		"rebuilding order_status must not change order count")
	assert.Equal(t, "confirmed", currentStatusOf(after, "order-A"),
		"order-A's transition must SURVIVE a order_status rebuild (disjoint reset + per-spec filter)")
	assert.Equal(t, "pending", currentStatusOf(after, "order-B"),
		"order-B remains pending after rebuild")

	// -- Phase 3 (reverse direction): rebuild ONLY order_transition. Its onReset
	// clears the transition (latest) sub-view; replay re-applies the
	// order-status-changed entry and SKIPS the foreign order-created entries. The
	// created sub-view is untouched, so order-B (created-only, never transitioned)
	// must still be pending and order-A still confirmed. This proves disjoint
	// reset holds in BOTH directions. --
	require.NoError(t, transitionCoord.Rebuild(ctx))
	testwait.External(t, "orderprojection-rebuild-order-transition",
		func() bool { return transitionCoord.Phase() == projection.PhaseLive },
		testtime.EventuallyLong, testtime.FastPoll, "phase != PhaseLive")

	afterReverse := svc.Query(ctx)
	assert.Equal(t, before.TotalOrders, afterReverse.TotalOrders,
		"rebuilding order_transition must not change order count")
	assert.Equal(t, "pending", currentStatusOf(afterReverse, "order-B"),
		"order-B's created sub-view must SURVIVE a order_transition rebuild (disjoint reset)")
	assert.Equal(t, "confirmed", currentStatusOf(afterReverse, "order-A"),
		"order-A's transition is rebuilt from its own stream")
	_ = eCreatedA
	_ = eCreatedB
}
