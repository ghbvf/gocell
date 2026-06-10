package devicecell

// cert_renewal_e2e_test.go — archetype-② (reconcile → command) end-to-end for
// #1757. Drives the full chain a near-expiry certificate takes:
//
//	cert store (near-expiry) → reconcile tick → command.EmitAsync → outbox store
//	→ relay (Claimer-wrapped dispatch, #1698) → enqueue handler → device command
//	queue → device dequeue.
//
// The headline invariant is CROSS-TICK DEDUP at the cert store: the first tick
// emits one rotate-cert entry and marks the cert's epoch renewal-requested, so the
// second tick's ScanNearExpiry skips the same un-renewed cert and emits nothing.
// One entry is written, the relay dispatches it once, the enqueue handler runs
// exactly once, and exactly one rotate-cert command lands in the queue across two
// ticks — single-emit per epoch that does NOT rely on the relay's 24h command-done
// TTL. (The relay Claimer is a secondary same-window backstop, exercised by
// command_dedup_e2e_test.go.)

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
	devicecertrenewal "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecertrenewal"
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
// assert the handler runs exactly once (store-level dedup writes a single entry
// the relay dispatches once) while the wrapped adapter still performs the real
// queue write the device later dequeues.
type countingEnqueueHandler struct {
	inner cmdenqueue.Handler
	calls int
}

func (h *countingEnqueueHandler) HandleEnqueue(ctx context.Context, req *cmdenqueue.Request) (*cmdenqueue.Response, error) {
	h.calls++
	return h.inner.HandleEnqueue(ctx, req)
}

// NOTE: no goleak here — this test starts the real outbox relay, whose internal
// worker goroutines are not deterministically joined when Relay.Start returns on
// ctx cancel (the relay is a long-lived service). This matches the sibling relay
// E2E command_dedup_e2e_test.go, which also omits goleak. reconcile.Loop goroutine
// hygiene is covered separately by the goleak'd loop tests (sweeper_lifecycle_test.go).
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
	reconciler, err := devicecertrenewal.NewReconciler(fc, certStore, kout.WrapEmitterForCell(we),
		kout.DemoCellTxManager(), 7*24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	// Two ticks: the same un-renewed cert is observed each interval, but the first
	// tick marks its epoch renewal-requested so the second tick's scan skips it —
	// only one command entry is written (store-level cross-tick dedup, no relay TTL).
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	rows := emitStore.Snapshot()
	require.Len(t, rows, 1, "store-level per-epoch dedup: the second tick skips the already-requested cert")
	_, ok0 := command.ClaimKeyFromEntry(rows[0].Entry)
	require.True(t, ok0, "the rotate-cert entry carries an idempotency claim key")
	assert.Equal(t, "dev-1", rows[0].Entry.AggregateID(), "the single entry is the device's renewal command")

	// Relay with Claimer-wrapped dispatch (#1698) over the emit store.
	relay := outbox.NewRelay(clock.Real(), emitStore, &kout.DiscardPublisher{},
		outbox.RelayConfig{PollInterval: 5 * time.Millisecond, BaseRetryDelay: 5 * time.Millisecond}.WithDefaults())
	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		cmdenqueue.DispatchID: cmdenqueue.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))

	relayCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); _ = relay.Start(relayCtx) }()

	require.NoError(t, emitStore.WaitFor(relayCtx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 1 && rows[0].Status == kout.StatePublished
	}), "the single command entry settles to published (dispatched once)")

	// Stop the relay and wait for Start to return before the remaining assertions,
	// so the dequeue checks below do not race the relay (relayDone gates Start's
	// return deterministically rather than relying on the ctx timeout).
	cancel()
	<-relayDone

	assert.Equal(t, 1, handler.calls, "enqueue handler runs exactly once across the two ticks (store-level cross-tick dedup)")

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
