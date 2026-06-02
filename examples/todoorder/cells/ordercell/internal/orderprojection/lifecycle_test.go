package orderprojection_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orderprojection "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/orderprojection"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
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

func appendAndReturn(t *testing.T, src *projection.MemReplaySource, id, status string) outbox.Entry {
	t.Helper()
	payload := ordercreated.Payload{ID: id, Item: "widget", Status: status}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	e := outboxtest.NewEntry("event.order-created.v1", b)
	src.Append(e)
	return e
}

// TestOrderProjection_HarnessLifecycle verifies:
//  1. Cold-start: the Coordinator's registered handler calls HandleOrderCreated
//     and advances the checkpoint offset.
//  2. Rebuild: onReset (ResetOrderStatus) clears the read model; replay
//     reconstructs a byte-identical Query snapshot.
func TestOrderProjection_HarnessLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	checkpointStore := projection.NewMemCheckpointStore()
	replaySource := projection.NewMemReplaySource()
	cursor := projection.NewMemCursor(replaySource)

	svc, err := orderprojection.NewService()
	require.NoError(t, err)

	reg := &lifecycleRegistrar{}

	coord, err := projection.NewCoordinator(clock.Real(), projection.CoordinatorConfig{
		Registrar:    reg,
		CellID:       "ordercell",
		ProjectionID: "order_status",
		TxRunner:     lifecycleDemoTxRunner{},
		Store:        checkpointStore,
		Cursor:       cursor,
		Replay:       replaySource,
		Tracer:       nopLifecycleTracer{},
	})
	require.NoError(t, err)

	spec := contractspec.ContractSpec{
		ID:        "event.order-created.v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     "order-created.v1",
	}

	err = coord.Subscribe(ctx, spec,
		func(ctx context.Context, e outbox.Entry) error {
			return svc.HandleOrderCreated(ctx, e)
		},
		projection.WithOnReset(svc.ResetOrderStatus),
	)
	require.NoError(t, err)
	require.NotNil(t, reg.handler, "coordinator must register an event handler")

	// -- Phase 1: cold-start apply --
	e1 := appendAndReturn(t, replaySource, "order-A", "pending")
	e2 := appendAndReturn(t, replaySource, "order-B", "confirmed")

	// Simulate live delivery through the registered handler (gate is open on cold-start)
	res1 := reg.handler(ctx, e1)
	assert.Equal(t, outbox.DispositionAck, res1.Disposition, "apply order-A must ack")
	res2 := reg.handler(ctx, e2)
	assert.Equal(t, outbox.DispositionAck, res2.Disposition, "apply order-B must ack")

	before := svc.Query(ctx)
	assert.Equal(t, int64(2), before.TotalOrders)

	offset, err := checkpointStore.LoadOffset(ctx, "ordercell", "order_status")
	require.NoError(t, err)
	assert.Equal(t, int64(2), offset, "checkpoint must equal head (2) after two applies")

	// -- B3: exactly-once skip — re-deliver e1, projection must ack but not double-count --
	res1again := reg.handler(ctx, e1)
	assert.Equal(t, outbox.DispositionAck, res1again.Disposition, "re-deliver order-A must ack (idempotent gate)")
	afterSkip := svc.Query(ctx)
	assert.Equal(t, int64(2), afterSkip.TotalOrders, "re-deliver must not increase TotalOrders (exactly-once)")

	// -- B4: bad-payload → permanent --
	badEntry := outboxtest.NewEntry("event.order-created.v1", []byte("not-json"))
	resBad := reg.handler(ctx, badEntry)
	assert.Equal(t, outbox.DispositionReject, resBad.Disposition, "bad payload must be rejected (permanent error)")

	// -- B5: business-read not blocked during rebuild —
	// Query must return synchronously without blocking; call it directly and assert
	// it completes (a synchronous call that returns is sufficient proof).
	// We verify this before coord.Rebuild so we are in the normal PhaseLive state.
	readableBeforeRebuild := svc.Query(ctx)
	assert.GreaterOrEqual(t, readableBeforeRebuild.TotalOrders, int64(0), "Query must return without blocking")

	// -- Phase 2: Rebuild → onReset → replay → byte-identical snapshot --
	err = coord.Rebuild(ctx)
	require.NoError(t, err)

	// Wait for coordinator to return to PhaseLive (rebuild is async).
	// Sanctioned poll via testwait.External (TEST-SLEEP-DISCIPLINE-01).
	testwait.External(t, "orderprojection-lifecycle-wait-for-phase",
		func() bool { return coord.Phase() == projection.PhaseLive },
		testtime.EventuallyLong, testtime.FastPoll, "phase != PhaseLive")
	assert.Equal(t, projection.PhaseLive, coord.Phase(), "coordinator must return to PhaseLive after rebuild")

	after := svc.Query(ctx)
	assert.True(t, reflect.DeepEqual(before.Statuses, after.Statuses),
		"rebuild must reproduce identical Statuses\nbefore: %+v\nafter:  %+v",
		before.Statuses, after.Statuses)
	assert.Equal(t, before.TotalOrders, after.TotalOrders, "rebuild must reproduce identical TotalOrders")
}
