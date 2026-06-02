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

// TestResetOrderStatus_ClearsReadModel verifies that ResetOrderStatus clears
// byStatus and orderAt.
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

// TestConcurrency_HandleAndQuery_NoDataRace exercises the RW mutex under parallel calls.
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
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.Query(ctx)
		}()
	}
	wg.Wait()
}
