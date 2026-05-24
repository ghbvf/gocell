// Package orderprojection slice tests.
// Full projection service unit tests (including internal store access) live in
// the shared package: cells/ordercell/internal/orderprojection.
// This file exercises the public surface exported by the slice alias.
package orderprojection

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	orderstatuschanged "github.com/ghbvf/gocell/generated/contracts/event/order-status-changed/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// helpers reused by handler_test.go and contract_test.go in this package.

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

// TestSliceAlias_NewService verifies the slice alias correctly delegates to the
// internal package: NewService works and the returned *Service responds to
// HandleOrderCreated and Query.
func TestSliceAlias_NewService(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Use distinct statuses to exercise makeCreatedEntry with different values.
	result := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "alias-order-1", "pending"))
	assert.Equal(t, outbox.Ack(), result)
	result2 := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "alias-order-2", "confirmed"))
	assert.Equal(t, outbox.Ack(), result2)

	summary := svc.Query(ctx)
	assert.Equal(t, int64(2), summary.TotalOrders)
}

// TestSliceAlias_WithLogger verifies WithLogger option propagates through the alias.
func TestSliceAlias_WithLogger(t *testing.T) {
	svc, err := NewService(WithLogger(nil)) // nil logger falls back to slog.Default()
	require.NoError(t, err)
	ctx := context.Background()
	result := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "alias-logger-1", "pending"))
	assert.Equal(t, outbox.Ack(), result)
}

// TestSliceAlias_Rebuild verifies Rebuild delegates through the alias.
func TestSliceAlias_Rebuild(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "rebuild-order-1", "pending"))

	report, err := svc.Rebuild(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.EventsReplayed)
}
