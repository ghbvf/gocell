package devicecell

// cert_renewal_e2e_test.go — archetype-② (reconcile → command) end-to-end for
// #1757. Drives the full chain a near-expiry certificate takes:
//
//	cert store (near-expiry) → reconcile tick → command.EmitAsync → outbox store
//	→ relay (Claimer-wrapped dispatch, #1698) → enqueue handler → device command
//	queue → device dequeue.
//
// The headline invariant is CROSS-TICK DEDUP: the reconciler emits one command
// entry PER tick for the same un-renewed cert, but the relay's Claimer dedups by
// the (tenant, deviceID, commandID) key — commandID derived from (deviceID,
// epoch) — so the enqueue handler runs exactly once and exactly one rotate-cert
// command lands in the queue across two ticks.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecert"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	devicecommand "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecommand"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/query"
	command "github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// countingEnqueueHandler decorates the real enqueue adapter so the test can
// assert relay-level dedup (handler runs exactly once) while the wrapped adapter
// still performs the real queue write the device later dequeues.
type countingEnqueueHandler struct {
	inner cmdenqueue.Handler
	calls int
}

func (h *countingEnqueueHandler) HandleEnqueue(ctx context.Context, req *cmdenqueue.Request) (*cmdenqueue.Response, error) {
	h.calls++
	return h.inner.HandleEnqueue(ctx, req)
}

func TestCertRenewal_CrossTickDedupToDequeue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(base)

	// Device + its near-expiry cert. The enqueue handler validates the device
	// exists, so it must be in the repo.
	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{ID: "dev-1", Name: "edge-sensor", Status: "online"}))
	certStore := devicecert.NewStore()
	_, err := certStore.Issue(ctx, "dev-1", base.Add(24*time.Hour)) // expires within the 7d threshold
	require.NoError(t, err)

	// Real consumer side: enqueue adapter over a devicecmd.Service backed by an
	// in-memory command queue — the production handler the relay dispatches to.
	queue := commandtest.NewInMemQueue()
	queue.Now = fc.Now
	codec := newTestCursorCodec(t)
	pubSvc, err := devicecmd.NewService(fc, queue, repo, codec, slog.New(slog.NewTextHandler(io.Discard, nil)),
		query.RunModeForDemo(true), devicecmd.WithSliceName("devicecommand"))
	require.NoError(t, err)
	reg := command.NewRegistry()
	handler := &countingEnqueueHandler{inner: devicecommand.EnqueueCommandAdapter{S: pubSvc}}
	require.NoError(t, cmdenqueue.Register(reg, handler))

	// Producer side: the cert-renewal reconciler emitting into the SAME store the
	// relay polls.
	emitStore := outboxtest.NewFakeStore()
	we, err := kout.NewWriterEmitter(emitStore)
	require.NoError(t, err)
	reconciler, err := devicecert.NewReconciler(fc, certStore, kout.WrapEmitterForCell(we),
		kout.DemoCellTxManager(), 7*24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	// Two ticks: the same un-renewed cert is observed each interval.
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	rows := emitStore.Snapshot()
	require.Len(t, rows, 2, "each tick writes one rotate-cert command entry for the near-expiry cert")
	k0, ok0 := command.ClaimKeyFromEntry(rows[0].Entry)
	k1, ok1 := command.ClaimKeyFromEntry(rows[1].Entry)
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, k0, k1, "same (device, epoch) across ticks must derive the SAME claim key")
	assert.NotEqual(t, rows[0].Entry.ID(), rows[1].Entry.ID(), "store ids differ")

	// Relay with Claimer-wrapped dispatch (#1698) over the emit store.
	relay := outbox.NewRelay(clock.Real(), emitStore, &kout.DiscardPublisher{},
		outbox.RelayConfig{PollInterval: 5 * time.Millisecond, BaseRetryDelay: 5 * time.Millisecond}.WithDefaults())
	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		cmdenqueue.DispatchID: cmdenqueue.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))

	relayCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	go func() { _ = relay.Start(relayCtx) }()

	require.NoError(t, emitStore.WaitFor(relayCtx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 2 &&
			rows[0].Status == kout.StatePublished &&
			rows[1].Status == kout.StatePublished
	}), "both command entries settle to published (one dispatched, one deduped)")

	assert.Equal(t, 1, handler.calls, "enqueue handler runs exactly once across the two ticks (cross-tick dedup)")

	// Device dequeue: exactly one rotate-cert command is claimable for the device.
	dequeued, err := queue.Dequeue(ctx, "dev-1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, dequeued, 1, "exactly one rotate-cert command is enqueued for the device")
	assert.Equal(t, "rotate-cert", dequeued[0].CommandType)

	// And the queue holds no other active command for the device.
	active, err := queue.ScanActive(ctx, kcommand.ScanFilter{DeviceID: "dev-1"})
	require.NoError(t, err)
	assert.Len(t, active, 1, "no duplicate rotate-cert command leaked across ticks")
}
