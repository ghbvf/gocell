package orderprojection

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	orderstatuschanged "github.com/ghbvf/gocell/generated/contracts/event/order-status-changed/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
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
	return outbox.Entry{ID: "entry-" + id, Payload: b}
}

func makeStatusChangedEntry(t *testing.T, id string) outbox.Entry {
	t.Helper()
	payload := orderstatuschanged.Payload{ID: id, OldStatus: domain.StatusPending, NewStatus: domain.StatusConfirmed}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return outbox.Entry{ID: "entry-sc-" + id, Payload: b}
}

func TestHandleOrderCreated_AddsToPendingBucket(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	result := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))

	assert.Equal(t, outbox.Ack(), result)
	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)
	require.Len(t, summary.Statuses, 1)
	assert.Equal(t, "pending", summary.Statuses[0].Status)
	assert.Equal(t, int64(1), summary.Statuses[0].Count)
	assert.Equal(t, []string{"order-1"}, summary.Statuses[0].OrderIDs)
	assert.Equal(t, int64(0), summary.LastAppliedSeq)
}

func TestHandleOrderStatusChanged_MovesFromPendingToConfirmed(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))
	result := svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1"))

	assert.Equal(t, outbox.Ack(), result)
	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)

	// pending bucket should be empty, confirmed should have 1
	pendingCount := int64(0)
	confirmedCount := int64(0)
	for _, s := range summary.Statuses {
		switch s.Status {
		case "pending":
			pendingCount = s.Count
		case "confirmed":
			confirmedCount = s.Count
		}
	}
	assert.Equal(t, int64(0), pendingCount)
	assert.Equal(t, int64(1), confirmedCount)
	assert.Equal(t, int64(1), summary.LastAppliedSeq)
}

func TestHandleOrderCreated_DecodeError_RejectsWithPermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	entry := outbox.Entry{ID: "bad-entry", Payload: []byte("not-json")}
	result := svc.HandleOrderCreated(ctx, entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	var pe *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &pe), "expected permanent error")

	// projection unchanged
	summary := svc.Query(ctx)
	assert.Equal(t, int64(0), summary.TotalOrders)
	assert.Empty(t, summary.Statuses)
}

func TestHandleOrderStatusChanged_DecodeError_RejectsWithPermanentError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	entry := outbox.Entry{ID: "bad-sc-entry", Payload: []byte("not-json")}
	result := svc.HandleOrderStatusChanged(ctx, entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	var pe2 *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &pe2), "expected permanent error")
}

func TestHandleOrderCreated_Idempotent_SameIDNoOp(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))
	// replay same event
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))

	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)
	// log should have only one entry for order-1 created
	svc.store.mu.RLock()
	logLen := len(svc.store.log)
	svc.store.mu.RUnlock()
	assert.Equal(t, 1, logLen)
}

func TestHandleOrderStatusChanged_Idempotent_SameIDSameNewStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1"))
	// replay same status change
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1"))

	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)
	svc.store.mu.RLock()
	logLen := len(svc.store.log)
	svc.store.mu.RUnlock()
	assert.Equal(t, 2, logLen) // created + one status-changed
}

func TestHandleOrderStatusChanged_OutOfOrder_ConvergentToNewStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// status-changed arrives before created
	result := svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1"))
	assert.Equal(t, outbox.Ack(), result)

	summary := svc.Query(ctx)
	assert.Equal(t, int64(1), summary.TotalOrders)

	confirmedCount := int64(0)
	for _, s := range summary.Statuses {
		if s.Status == "confirmed" {
			confirmedCount = s.Count
		}
	}
	assert.Equal(t, int64(1), confirmedCount, "out-of-order event should converge to newStatus bucket")
}

func TestQuery_StatusesSortedByName(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "zz-status"))
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "aa-status"))
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-3", "mm-status"))

	summary := svc.Query(ctx)
	require.Len(t, summary.Statuses, 3)
	assert.Equal(t, "aa-status", summary.Statuses[0].Status)
	assert.Equal(t, "mm-status", summary.Statuses[1].Status)
	assert.Equal(t, "zz-status", summary.Statuses[2].Status)
}

func TestQuery_TotalOrdersAndLastAppliedSeq(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "pending"))
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1"))

	summary := svc.Query(ctx)
	assert.Equal(t, int64(2), summary.TotalOrders)
	assert.Equal(t, int64(2), summary.LastAppliedSeq)
}

func TestRebuild_Idempotent_SummaryDeepEqual(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "pending"))
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1"))

	before := svc.Query(ctx)

	_, err := svc.Rebuild(ctx)
	require.NoError(t, err)

	after := svc.Query(ctx)
	assert.True(t, reflect.DeepEqual(before, after), "rebuild must reproduce canonical summary")
}

func TestRebuild_FixesCorruption(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "pending"))

	// manually corrupt the store (package-internal test access)
	svc.store.mu.Lock()
	svc.store.byStatus = map[string][]string{"wrong": {"garbage"}}
	svc.store.orderAt = map[string]string{}
	svc.store.mu.Unlock()

	_, err := svc.Rebuild(ctx)
	require.NoError(t, err)

	summary := svc.Query(ctx)
	assert.Equal(t, int64(2), summary.TotalOrders)
	for _, s := range summary.Statuses {
		assert.Equal(t, "pending", s.Status)
		assert.Equal(t, int64(2), s.Count)
	}
}

func TestRebuild_EmptyLog_ZeroReport(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	report, err := svc.Rebuild(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, report.EventsReplayed)
	assert.Equal(t, 0, report.StatusesRebuilt)
	assert.Equal(t, int64(-1), report.LastAppliedSeq)

	summary := svc.Query(ctx)
	assert.Equal(t, int64(0), summary.TotalOrders)
	assert.Empty(t, summary.Statuses)
}

func TestRebuild_ReportCounts(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-1", "pending"))
	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "order-2", "pending"))
	svc.HandleOrderStatusChanged(ctx, makeStatusChangedEntry(t, "order-1"))

	svc.store.mu.RLock()
	logLen := len(svc.store.log)
	svc.store.mu.RUnlock()

	report, err := svc.Rebuild(ctx)
	require.NoError(t, err)
	assert.Equal(t, logLen, report.EventsReplayed)
	assert.Equal(t, int64(2), report.LastAppliedSeq)
	assert.Equal(t, 2, report.StatusesRebuilt) // "pending" + "confirmed"
}

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
			svc.HandleOrderCreated(ctx, makeCreatedEntry(t, id, "pending"))
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
