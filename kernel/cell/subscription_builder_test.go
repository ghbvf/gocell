package cell

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestSubscriptionBuilder_HappyPath_AppendsToSnapshot verifies the fluent
// builder produces the same SubscriptionRequest as the positional Subscribe.
func TestSubscriptionBuilder_HappyPath_AppendsToSnapshot(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	spec := testRegistrySpec("order.placed")

	err := rec.Subscription(spec).
		CellID("ordercell").
		ConsumerGroup("cg-order").
		Handler(noopHandler).
		SliceID("order-slice").
		Register()
	require.NoError(t, err)

	snap := rec.Snapshot()
	require.Len(t, snap.Subscriptions, 1)
	assert.Equal(t, spec, snap.Subscriptions[0].Spec)
	assert.Equal(t, "cg-order", snap.Subscriptions[0].ConsumerGroup)
	assert.Equal(t, "ordercell", snap.Subscriptions[0].CellID)
	assert.Equal(t, "order-slice", snap.Subscriptions[0].SliceID)
	assert.NotNil(t, snap.Subscriptions[0].Handler)
}

// TestSubscriptionBuilder_ConsumerGroupDefaultsToCellID verifies the common
// consumerGroup==cellID case works without an explicit .ConsumerGroup() call
// (cellgen-consistent: cell.tmpl defaults ConsumerGroupDefault to cellID).
func TestSubscriptionBuilder_ConsumerGroupDefaultsToCellID(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	spec := testRegistrySpec("order.placed")

	err := rec.Subscription(spec).
		CellID("ordercell").
		Handler(noopHandler).
		Register()
	require.NoError(t, err)

	snap := rec.Snapshot()
	require.Len(t, snap.Subscriptions, 1)
	assert.Equal(t, "ordercell", snap.Subscriptions[0].ConsumerGroup,
		"ConsumerGroup defaults to CellID when .ConsumerGroup() is omitted")
	assert.Equal(t, "ordercell", snap.Subscriptions[0].CellID)
	assert.Empty(t, snap.Subscriptions[0].SliceID, "SliceID must be empty when .SliceID() is omitted")
}

// TestSubscriptionBuilder_Register_RoutesThroughValidationFunnel verifies that
// .Register() delegates to the single RegistryRecorder.Subscribe validation
// funnel (no duplicated validation in the builder).
func TestSubscriptionBuilder_Register_RoutesThroughValidationFunnel(t *testing.T) {
	tests := []struct {
		name     string
		build    func(*RegistryRecorder) error
		wantWord string
	}{
		{
			name: "nil handler rejected",
			build: func(r *RegistryRecorder) error {
				return r.Subscription(testRegistrySpec("o")).CellID("c").ConsumerGroup("g").Register()
			},
			wantWord: "handler",
		},
		{
			name: "non-event spec rejected",
			build: func(r *RegistryRecorder) error {
				spec := contractspec.ContractSpec{
					ID: "http.foo.v1", Kind: cellvocab.ContractHTTP, Transport: "amqp", Topic: "foo",
				}
				return r.Subscription(spec).CellID("c").ConsumerGroup("g").Handler(noopHandler).Register()
			},
			wantWord: "kind",
		},
		{
			name: "empty cellID rejected",
			build: func(r *RegistryRecorder) error {
				return r.Subscription(testRegistrySpec("o")).CellID("").Handler(noopHandler).Register()
			},
			// CellID("") causes consumerGroup to also default to "", so the
			// consumerGroup check fires first in Subscribe (before cellID check).
			wantWord: "consumergroup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
			err := tt.build(rec)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Contains(t, strings.ToLower(ecErr.Message), tt.wantWord)
		})
	}
}

// TestSubscriptionBuilder_PostSnapshot_Register_Panics verifies the terminal
// .Register() inherits the recorder's mustNotBeFinalized guard (it routes
// through Subscribe).
func TestSubscriptionBuilder_PostSnapshot_Register_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		_ = rec.Subscription(testRegistrySpec("o")).CellID("c").Handler(noopHandler).Register()
	})
}

// TestSubscriptionBuilder_Subscription_OnRegistrarInterface verifies the builder
// entry point is reachable through the cell.Registrar interface, which is the
// type external cells receive in Cell.Init(ctx, reg).
func TestSubscriptionBuilder_Subscription_OnRegistrarInterface(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	var reg Registrar = rec

	err := reg.Subscription(testRegistrySpec("order.placed")).
		CellID("ordercell").
		Handler(noopHandler).
		Register()
	require.NoError(t, err)
	require.Len(t, rec.Snapshot().Subscriptions, 1)
}
