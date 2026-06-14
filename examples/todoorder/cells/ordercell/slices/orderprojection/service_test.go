// Package orderprojection slice tests.
// Full projection service unit tests live in cells/ordercell/internal/orderprojection.
// This file exercises the public surface exported by the slice alias.
package orderprojection

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
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
	return outboxtest.NewEntry("event.order-created.v1", b)
}

// TestSliceAlias_NewService verifies the slice alias correctly delegates to the
// internal package: NewService works and the returned *Service responds to
// HandleOrderCreated and Query.
func TestSliceAlias_NewService(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err1 := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "alias-order-1", "pending"))
	require.NoError(t, err1)
	err2 := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "alias-order-2", "confirmed"))
	require.NoError(t, err2)

	summary := svc.Query(ctx)
	assert.Equal(t, int64(2), summary.TotalOrders)
}

// TestSliceAlias_WithLogger verifies WithLogger option propagates through the alias.
func TestSliceAlias_WithLogger(t *testing.T) {
	svc, err := NewService(WithLogger(nil)) // nil logger falls back to slog.Default()
	require.NoError(t, err)
	ctx := context.Background()
	applyErr := svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "alias-logger-1", "pending"))
	require.NoError(t, applyErr)
}

// TestSliceAlias_ResetOrderStatus verifies ResetOrderStatus delegates through the alias.
func TestSliceAlias_ResetOrderStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.HandleOrderCreated(ctx, makeCreatedEntry(t, "reset-order-1", "pending")))
	require.NoError(t, svc.ResetOrderStatus(ctx))

	summary := svc.Query(ctx)
	assert.Equal(t, int64(0), summary.TotalOrders)
}
