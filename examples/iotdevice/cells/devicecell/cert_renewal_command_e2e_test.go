package devicecell

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecert"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	commandruntime "github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/eventbus"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

func TestDeviceCell_CertRenewal_ReconcileToAsyncEnqueueDedupAndDequeue(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	fc := clockmock.New(base)
	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID:            "dev-cert",
		Name:          "cert-sensor",
		Status:        "online",
		LastSeen:      base,
		CertEpoch:     5,
		CertExpiresAt: base.Add(devicecert.RenewalThreshold).Add(-time.Hour),
	}))

	store := outboxtest.NewFakeStore()
	writerEmitter, err := kout.NewWriterEmitter(store)
	require.NoError(t, err)
	reg := commandruntime.NewRegistry()
	queue := commandtest.NewInMemQueue()
	queue.Now = fc.Now
	cell := NewDeviceCell(
		fc,
		WithDeviceRepository(repo),
		WithDirectPublisher(kout.WrapPublisherForCell(eventbus.New(clock.Real()))),
		WithBootstrapEmitter(kout.WrapEmitterForCell(writerEmitter)),
		WithCommandRegistry(reg),
	)
	cell.RegisterCommandQueue(queue)
	rec := newTestRec()
	require.NoError(t, cell.Init(ctx, rec))

	relay := outboxruntime.NewRelay(clock.Real(), store, &kout.DiscardPublisher{},
		outboxruntime.RelayConfig{PollInterval: 5 * time.Millisecond, BaseRetryDelay: 5 * time.Millisecond}.WithDefaults())
	relay.WithCommandDispatch(reg, map[commandruntime.CommandID]commandruntime.AsyncDispatchFunc{
		cmdenqueue.DispatchID: cmdenqueue.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))
	relayCtx, cancelRelay := context.WithCancel(ctx)
	defer cancelRelay()
	go func() { _ = relay.Start(relayCtx) }()

	stop := startNamedLifecycleHook(t, rec, "devicecert.renewal", ctx)
	defer func() { require.NoError(t, stop(context.Background())) }()

	fc.Advance(certRenewalInterval)
	fc.Advance(certRenewalInterval)

	waitCtx, cancelWait := context.WithTimeout(ctx, 3*time.Second)
	defer cancelWait()
	require.NoError(t, store.WaitFor(waitCtx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 2 &&
			rows[0].Status == kout.StatePublished &&
			rows[1].Status == kout.StatePublished
	}), "both repeated cert-renewal command entries must settle to published")

	rows := store.Snapshot()
	key0, ok0 := commandruntime.ClaimKeyFromEntry(rows[0].Entry)
	key1, ok1 := commandruntime.ClaimKeyFromEntry(rows[1].Entry)
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, key0, key1, "same device cert epoch must dedupe across ticks")

	got, err := queue.Dequeue(ctx, "dev-cert", 10, kcommand.DefaultLeaseDuration)
	require.NoError(t, err)
	require.Len(t, got, 1, "relay Claimer must allow exactly one queued rotate-cert command")
	assert.Equal(t, devicecert.CommandTypeRotateCert, got[0].CommandType)

	var payload struct {
		DeviceID  string `json:"deviceId"`
		CertEpoch int64  `json:"certEpoch"`
	}
	require.NoError(t, json.Unmarshal(got[0].Payload, &payload))
	assert.Equal(t, "dev-cert", payload.DeviceID)
	assert.Equal(t, int64(5), payload.CertEpoch)
}

func startNamedLifecycleHook(
	t *testing.T, rec *cell.RegistryRecorder, name string, ctx context.Context,
) func(context.Context) error {
	t.Helper()
	for _, hook := range rec.Snapshot().LifecycleHooks {
		if hook.Name != name {
			continue
		}
		require.NoError(t, hook.OnStart(ctx), "start lifecycle hook %s", name)
		return hook.OnStop
	}
	t.Fatalf("lifecycle hook %q not registered", name)
	return nil
}
