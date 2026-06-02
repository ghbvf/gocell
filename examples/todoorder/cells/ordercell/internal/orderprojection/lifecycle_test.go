package orderprojection_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

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
	assert.Greater(t, offset, int64(0), "checkpoint must advance after two applies")

	// -- Phase 2: Rebuild → onReset → replay → byte-identical snapshot --
	err = coord.Rebuild(ctx)
	require.NoError(t, err)

	// Wait for coordinator to return to PhaseLive (rebuild is async).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if coord.Phase() == projection.PhaseLive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, projection.PhaseLive, coord.Phase(), "coordinator must return to PhaseLive after rebuild")

	after := svc.Query(ctx)
	assert.True(t, reflect.DeepEqual(before.Statuses, after.Statuses),
		"rebuild must reproduce identical Statuses\nbefore: %+v\nafter:  %+v",
		before.Statuses, after.Statuses)
	assert.Equal(t, before.TotalOrders, after.TotalOrders, "rebuild must reproduce identical TotalOrders")
}
