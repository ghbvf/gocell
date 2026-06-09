package devicecert

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/reconcile"
	rtcommand "github.com/ghbvf/gocell/runtime/command"
)

var certTestBase = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func TestReconciler_EmitsRotateCertCommandForNearExpiryDevice(t *testing.T) {
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID:            "dev-near",
		Name:          "near",
		Status:        "online",
		LastSeen:      certTestBase,
		CertEpoch:     7,
		CertExpiresAt: certTestBase.Add(RenewalThreshold).Add(-time.Hour),
	}))
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID:            "dev-later",
		Name:          "later",
		Status:        "online",
		LastSeen:      certTestBase,
		CertEpoch:     8,
		CertExpiresAt: certTestBase.Add(RenewalThreshold).Add(time.Hour),
	}))
	recorder := outboxtest.NewRecorder()
	r, err := NewReconciler(clockmock.New(certTestBase), repo, recorder.CellEmitter(), kout.DemoCellTxManager())
	require.NoError(t, err)

	res, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)

	entries := recorder.Entries()
	require.Len(t, entries, 1)
	entry := entries[0]
	assert.Equal(t, string(cmdenqueue.DispatchID), entry.RoutingTopic())
	assert.Equal(t, "dev-near", entry.AggregateID())
	assert.Equal(t, RenewalCommandID("dev-near", 7), entry.Metadata()[rtcommand.CommandIDMetadataKey])

	var req cmdenqueue.Request
	require.NoError(t, json.Unmarshal(entry.Payload(), &req))
	assert.Equal(t, "dev-near", req.DeviceID)
	assert.Equal(t, CommandTypeRotateCert, req.CommandType)

	var payload rotateCertPayload
	require.NoError(t, json.Unmarshal([]byte(req.Payload), &payload))
	assert.Equal(t, "dev-near", payload.DeviceID)
	assert.Equal(t, int64(7), payload.CertEpoch)
	assert.Equal(t, certTestBase, payload.RequestedAt)
}

func TestReconciler_RepeatedTickDerivesSameClaimKeyForSameEpoch(t *testing.T) {
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID:            "dev-repeat",
		Name:          "repeat",
		Status:        "online",
		LastSeen:      certTestBase,
		CertEpoch:     3,
		CertExpiresAt: certTestBase.Add(RenewalThreshold).Add(-time.Hour),
	}))
	recorder := outboxtest.NewRecorder()
	r, err := NewReconciler(clockmock.New(certTestBase), repo, recorder.CellEmitter(), kout.DemoCellTxManager())
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	entries := recorder.Entries()
	require.Len(t, entries, 2)
	key0, ok0 := rtcommand.ClaimKeyFromEntry(entries[0])
	key1, ok1 := rtcommand.ClaimKeyFromEntry(entries[1])
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, key0, key1)
	assert.NotEqual(t, entries[0].ID(), entries[1].ID())
}
