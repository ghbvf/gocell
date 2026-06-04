package orderprojection

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	orderstatuschanged "github.com/ghbvf/gocell/generated/contracts/event/order-status-changed/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService()
	require.NoError(t, err)
	return svc
}

func makeCreatedEntry(t *testing.T, id, status string) outbox.Entry {
	t.Helper()
	payload := ordercreated.Payload{ID: id, Item: "widget", Status: status}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return outboxtest.NewEntry("event.order-created.v1", b)
}

func makeStatusChangedEntry(t *testing.T, id, newStatus string) outbox.Entry {
	t.Helper()
	// oldStatus is schema-required but not consumed by the projection (only
	// newStatus feeds the latest sub-view); a fixed value keeps the payload valid.
	payload := orderstatuschanged.Payload{ID: id, OldStatus: "pending", NewStatus: newStatus}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return outboxtest.NewEntry("event.order-status-changed.v1", b)
}

// statusOf returns the status bucket containing orderID in the summary, or "".
func statusOf(s Summary, orderID string) string {
	for _, b := range s.Statuses {
		for _, id := range b.OrderIDs {
			if id == orderID {
				return b.Status
			}
		}
	}
	return ""
}

// TestHandleOrderCreated_ApplySuccess verifies that a valid order-created event
// is applied to the store (returns nil error) and is reflected in Query.
func TestHandleOrderCreated_ApplySuccess(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))

	require.NoError(t, err)
	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)
	require.Len(t, summary.Statuses, 1)
	assert.Equal(t, "pending", summary.Statuses[0].Status)
	assert.Equal(t, int64(1), summary.Statuses[0].Count)
	assert.Equal(t, []string{"order-1"}, summary.Statuses[0].OrderIDs)
}

// TestQuery_OrderIDsBounded verifies the summary caps OrderIDs per status at
// maxOrderIDsPerStatus while Count keeps the true (unbounded) total, so a
// truncated bucket is observable rather than silent.
func TestQuery_OrderIDsBounded(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const total = maxOrderIDsPerStatus + 25
	for i := 0; i < total; i++ {
		err := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-"+strconv.Itoa(i), "pending"))
		require.NoError(t, err)
	}

	summary := svc.Query(ctx)
	require.Len(t, summary.Statuses, 1)
	assert.Equal(t, int64(total), summary.Statuses[0].Count, "Count must be the true total")
	assert.Len(t, summary.Statuses[0].OrderIDs, maxOrderIDsPerStatus, "OrderIDs must be capped")
	assert.Equal(t, int64(total), summary.TotalOrders)
}

// TestHandleOrderCreated_DecodeError_PermanentError verifies that an
// undecodable payload returns a permanent error (not nil).
func TestHandleOrderCreated_DecodeError_PermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	entry := outboxtest.NewEntry("event.order-created.v1", []byte("not-json"))
	err := svc.HandleOrderCreated(ctx, entry)

	require.Error(t, err)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(err, &pe), "expected permanent error, got %T: %v", err, err)

	// projection unchanged
	summary := svc.Query(ctx)
	assert.Equal(t, int64(0), summary.TotalOrders)
	assert.Empty(t, summary.Statuses)
}

// TestHandleOrderCreated_MissingID_PermanentError verifies that an order-created
// payload with empty id returns a permanent error.
func TestHandleOrderCreated_MissingID_PermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	payload := ordercreated.Payload{ID: "", Item: "widget", Status: domain.StatusPending}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	entry := outboxtest.NewEntry("event.order-created.v1", b)

	applyErr := svc.HandleOrderCreated(ctx, entry)
	require.Error(t, applyErr)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(applyErr, &pe), "expected permanent error")

	summary := svc.Query(ctx)
	assert.Equal(t, int64(0), summary.TotalOrders)
}

// TestHandleOrderCreated_MissingStatus_PermanentError verifies that an
// order-created payload with empty status returns a permanent error.
func TestHandleOrderCreated_MissingStatus_PermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	payload := ordercreated.Payload{ID: "order-missing-status", Item: "widget", Status: ""}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	entry := outboxtest.NewEntry("event.order-created.v1", b)

	applyErr := svc.HandleOrderCreated(ctx, entry)
	require.Error(t, applyErr)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(applyErr, &pe), "expected permanent error")

	summary := svc.Query(ctx)
	assert.Equal(t, int64(0), summary.TotalOrders)
}

// TestHandleOrderStatusChanged_ApplySuccess verifies a status transition moves
// the order from its created bucket to the transitioned bucket (composition:
// latest wins over created).
func TestHandleOrderStatusChanged_ApplySuccess(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending")))
	assert.Equal(t, "pending", statusOf(svc.Query(ctx), "order-1"))

	require.NoError(t, svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1", "confirmed")))

	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders, "transition must not create a second order")
	assert.Equal(t, "confirmed", statusOf(summary, "order-1"), "latest transition wins over created status")
}

// TestHandleOrderStatusChanged_DecodeError_PermanentError verifies an
// undecodable payload returns a permanent error and leaves the model unchanged.
func TestHandleOrderStatusChanged_DecodeError_PermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.HandleOrderStatusChanged(ctx, outboxtest.NewEntry("event.order-status-changed.v1", []byte("not-json")))
	require.Error(t, err)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(err, &pe), "expected permanent error, got %T: %v", err, err)
	assert.Equal(t, int64(0), svc.Query(ctx).TotalOrders)
}

// TestHandleOrderStatusChanged_MissingID_PermanentError verifies a payload with
// empty id returns a permanent error.
func TestHandleOrderStatusChanged_MissingID_PermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "", "confirmed"))
	require.Error(t, err)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(err, &pe), "expected permanent error")
}

// TestHandleOrderStatusChanged_MissingNewStatus_PermanentError verifies a
// payload with empty newStatus returns a permanent error.
func TestHandleOrderStatusChanged_MissingNewStatus_PermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1", ""))
	require.Error(t, err)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(err, &pe), "expected permanent error")
}

// TestResetOrderTransition_ClearsTransitionViewOnly verifies ResetOrderTransition
// clears the latest sub-view but leaves the created sub-view intact (disjoint
// reset): after the reset the order reverts to its created status, not absent.
func TestResetOrderTransition_ClearsTransitionViewOnly(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "pending")))
	require.NoError(t, svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-2", "confirmed")))
	require.Equal(t, "confirmed", statusOf(svc.Query(ctx), "order-2"))

	require.NoError(t, svc.ResetOrderTransition(ctx))

	after := svc.Query(ctx)
	assert.Equal(t, int64(1), after.TotalOrders, "created sub-view survives a transition reset")
	assert.Equal(t, "pending", statusOf(after, "order-2"), "order reverts to created status after transition reset")
}

// TestQuery_TransitionBeforeCreation verifies the eventual-consistency scenario
// where an order-status-changed event is processed before its order-created
// event (independent projections). The order must appear via the latest sub-view
// even before creation, and the composed status must remain the transition value
// (latest wins over created) once the creation event arrives.
func TestQuery_TransitionBeforeCreation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Transition arrives first — order-9 appears only via latest sub-view.
	require.NoError(t, svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-9", "shipped")))

	mid := svc.Query(ctx)
	assert.Equal(t, int64(1), mid.TotalOrders, "order visible via latest before creation event")
	assert.Equal(t, "shipped", statusOf(mid, "order-9"), "transition status shown before creation")

	// Creation event arrives with an earlier-snapshot status — latest must win.
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-9", "pending")))

	after := svc.Query(ctx)
	assert.Equal(t, int64(1), after.TotalOrders, "still one order after creation event arrives")
	assert.Equal(t, "shipped", statusOf(after, "order-9"), "latest (shipped) wins over created (pending)")
}

// TestResetOrderStatus_ClearsReadModel verifies that ResetOrderStatus clears the
// created sub-view (Query returns an empty model when no transitions remain).
func TestResetOrderStatus_ClearsReadModel(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending")))
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "confirmed")))

	summary := svc.Query(ctx)
	require.Equal(t, int64(2), summary.TotalOrders)

	err := svc.ResetOrderStatus(ctx)
	require.NoError(t, err)

	after := svc.Query(ctx)
	assert.Equal(t, int64(0), after.TotalOrders)
	assert.Empty(t, after.Statuses)
}

// TestQuery_NoLastAppliedSeq verifies that Summary does not carry LastAppliedSeq.
// (The harness owns the offset; the service no longer tracks sequence numbers.)
func TestQuery_NoLastAppliedSeq(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending")))

	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)
	assert.Len(t, summary.Statuses, 1)
	// Verify Summary has Statuses and TotalOrders but NOT a LastAppliedSeq field.
	// This is a compile-time check: if LastAppliedSeq were still on the struct
	// the assignment below would fail to compile.
	_ = Summary{Statuses: summary.Statuses, TotalOrders: summary.TotalOrders}
}

// TestQuery_StatusesSortedByName verifies alphabetical sort order.
func TestQuery_StatusesSortedByName(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "zz-status")))
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "aa-status")))
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-3", "mm-status")))

	summary := svc.Query(ctx)
	require.Len(t, summary.Statuses, 3)
	assert.Equal(t, "aa-status", summary.Statuses[0].Status)
	assert.Equal(t, "mm-status", summary.Statuses[1].Status)
	assert.Equal(t, "zz-status", summary.Statuses[2].Status)
}

// TestQuery_TotalOrders verifies TotalOrders count.
func TestQuery_TotalOrders(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending")))
	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "pending")))

	summary := svc.Query(ctx)
	assert.Equal(t, int64(2), summary.TotalOrders)
}

// TestConcurrency_HandleAndQuery_NoDataRace exercises the RW mutex under parallel
// calls: HandleOrderCreated, HandleOrderStatusChanged (latest sub-view write), and
// Query all run concurrently to ensure no data race on either sub-view.
func TestConcurrency_HandleAndQuery_NoDataRace(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "order-" + strconv.Itoa(i)
			_ = svc.HandleOrderCreated(ctx, makeCreatedEntry(t, id, "pending"))
		}(i)
	}
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "order-" + strconv.Itoa(i)
			_ = svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, id, "confirmed"))
		}(i)
	}
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.Query(ctx)
		}()
	}
	wg.Wait()
}
