package devicecell

// cert_renewal_e2e_test.go — F1 (#1820) acceptance test.
//
// HEADLINE INVARIANT: an offline device (never dequeues) does NOT accumulate
// duplicate rotate-cert commands across reconcile ticks. The queue's
// active-uniqueness mechanism (IdempotencyKey + non-terminal guard) coalesces
// duplicate emits to no-ops, so the active command count stays exactly 1
// regardless of how many ticks fire.
//
// After the AttemptTTL elapses and the Sweeper expires the command (terminal),
// the active-uniqueness key is released. The next reconcile tick then admits a
// FRESH rotate-cert — proving level-triggered retry works without any
// producer-side state.
//
// NOTE: no goleak — goroutine hygiene is covered by the goleak'd loop tests
// in slices/devicecertrenewal/reconciler_loop_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	devicecertcompletion "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecertcompletion"
	devicecertrenewal "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecertrenewal"
	slicecmd "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecommand"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	kcommand "github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/kernel/command/commandtest"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	rtcommand "github.com/ghbvf/gocell/framework/runtime/command"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
)

// cert renewal e2e duration constants.
const (
	// certRenewalThreshold7d is the renewal threshold for F1 (offline device test).
	certRenewalThreshold7d = 7 * 24 * time.Hour
	// certNearExpiry24h is the near-expiry window seeded in test device cert fields.
	certNearExpiry24h = 24 * time.Hour
)

// deviceSelfCtx returns ctx with a device-self principal (PrincipalUser, Subject
// == deviceID) — the identity a device presents when acking its OWN rotate-cert.
// The completion hook (devicecertcompletion.OnCommandResolved) emits the
// rotation-resolved event only for such device-self acks, so the ack path must
// carry this principal for the convergence loop to close.
func deviceSelfCtx(ctx context.Context, deviceID string) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{Kind: auth.PrincipalUser, Subject: deviceID})
}

// dispatchNew dispatches all entries added to rec since the last call to
// rec.Reset(). For each entry it:
//   - derives the Claimer key via rtcommand.ClaimKeyFromEntry
//   - parses the deadline from CommandDeadlineMetadataKey
//   - injects (key, deadline) into ctx via rtcommand.WithDispatchedUniqueness
//   - calls the enqueue handler
//
// This simulates the relay's active-uniqueness injection path (relay's
// dispatchCommand, #1698) without starting any relay goroutines.
// After dispatching, rec is Reset so the next call only dispatches new entries.
func dispatchNew(t *testing.T, ctx context.Context, h cmdenqueue.Handler, rec *outboxtest.Recorder) {
	t.Helper()
	entries := rec.Entries()
	rec.Reset()
	for _, entry := range entries {
		// Derive the active-uniqueness Claimer key (same funnel the relay uses).
		key, ok := rtcommand.ClaimKeyFromEntry(entry)
		require.True(t, ok, "all rotate-cert entries must have a valid Claimer key")

		// Parse the deadline from the entry metadata (set by WithActiveUniqueness).
		dlRaw, hasDL := entry.Metadata()[rtcommand.CommandDeadlineMetadataKey]
		require.True(t, hasDL, "rotate-cert entry must carry CommandDeadlineMetadataKey")
		dl, err := time.Parse(time.RFC3339Nano, dlRaw)
		require.NoError(t, err, "CommandDeadlineMetadataKey must be RFC3339Nano")

		// Inject uniqueness into ctx (relay's WithDispatchedUniqueness call).
		dctx := rtcommand.WithDispatchedUniqueness(ctx, key, dl)

		// Parse the handler request from the entry payload.
		var req cmdenqueue.Request
		require.NoError(t, json.Unmarshal(entry.Payload(), &req), "payload must decode as cmdenqueue.Request")

		_, err = h.HandleEnqueue(dctx, &req)
		require.NoError(t, err, "enqueue handler must succeed")
	}
}

// TestCertRenewal_F1_OfflineDeviceNoDuplicates is the F1 acceptance test for
// #1820. It proves that:
//
//  1. Multiple reconcile ticks with an OFFLINE device (never dequeues) keep
//     exactly 1 active rotate-cert command in the queue — no accumulation.
//
//  2. After AttemptTTL elapses and the Sweeper expires the command, the next
//     tick enqueues a FRESH rotate-cert (retry works without producer state).
func TestCertRenewal_F1_OfflineDeviceNoDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(base)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Device repo: one offline device with a near-expiry cert.
	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID: "dev-offline", Name: "offline-sensor", Status: "online", LastSeen: base,
		CertEpoch: 1, CertExpiresAt: base.Add(certNearExpiry24h),
	}))

	// Producer: cert-renewal reconciler emitting into the Recorder.
	rec := outboxtest.NewRecorder()
	reconciler, err := devicecertrenewal.NewReconciler(fc, repo, rec.CellEmitter(),
		devicecertrenewal.Policy{
			Threshold:  certRenewalThreshold7d,
			AttemptTTL: certRenewalAttemptTTL,
		}, logger)
	require.NoError(t, err)

	// Consumer: in-memory command queue + devicecmd.Service + enqueue handler.
	// The device never calls Dequeue — it stays offline throughout.
	queue := commandtest.NewInMemQueue()
	queue.Now = fc.Now
	codec := newTestCursorCodec(t)
	svc, err := devicecmd.NewService(fc, queue, repo, codec, logger, query.RunModeForDemo(true),
		devicecmd.WithSliceName("devicecommand"))
	require.NoError(t, err)
	handler := slicecmd.EnqueueCommandAdapter{S: svc}

	// --- Phase 1: Three ticks, device stays offline. ---
	// Each tick: reconciler emits 1 entry; relay-sim dispatches it; queue
	// active-uniqueness coalesces duplicates → active count stays exactly 1.
	for tick := 1; tick <= 3; tick++ {
		_, err = reconciler.Reconcile(ctx, reconcile.Request{})
		require.NoError(t, err, "tick %d: reconcile must not error", tick)

		assert.Len(t, rec.Entries(), 1, "tick %d must emit exactly 1 entry", tick)
		dispatchNew(t, ctx, handler, rec)

		active, scanErr := queue.ScanActive(ctx, kcommand.ScanFilter{DeviceID: "dev-offline"})
		require.NoError(t, scanErr)
		assert.Len(t, active, 1,
			"tick %d: offline device must have exactly 1 active rotate-cert (no accumulation)", tick)
	}

	// Capture the first command's ID to confirm expiry later.
	active0, err := queue.ScanActive(ctx, kcommand.ScanFilter{DeviceID: "dev-offline"})
	require.NoError(t, err)
	require.Len(t, active0, 1)
	firstCmdID := active0[0].ID

	// --- Phase 2: Advance clock past AttemptTTL + run Sweeper → Expired. ---
	fc.Advance(certRenewalAttemptTTL + time.Second) // now > OverallDeadline of first command

	sweeper, err := kcommand.NewSweeper(queue, queue, fc)
	require.NoError(t, err)
	require.NoError(t, sweeper.SweepTick(ctx, fc.Now()), "sweeper must transition expired command")

	// The first command is now terminal (Expired); active count is 0.
	afterExpiry, err := queue.ScanActive(ctx, kcommand.ScanFilter{DeviceID: "dev-offline"})
	require.NoError(t, err)
	assert.Empty(t, afterExpiry, "active-uniqueness key released: no active commands after expiry")

	// GetCommand still finds the entry but it should be terminal.
	expiredEntry, err := queue.GetCommand(ctx, firstCmdID)
	require.NoError(t, err)
	assert.True(t, expiredEntry.Status.IsTerminal(),
		"the first command must be terminal (Expired) after SweepTick")

	// --- Phase 3: Next tick re-enqueues a fresh attempt. ---
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err, "post-expiry reconcile must not error")
	dispatchNew(t, ctx, handler, rec)

	afterRetry, err := queue.ScanActive(ctx, kcommand.ScanFilter{DeviceID: "dev-offline"})
	require.NoError(t, err)
	assert.Len(t, afterRetry, 1, "a fresh rotate-cert must be queued after the expired slot is released")
	assert.NotEqual(t, firstCmdID, afterRetry[0].ID,
		"the retry command is a different entry (fresh enqueue, not the expired one)")
}

// TestCertRenewal_CompletionClosesLoop is the #1870 HEADLINE acceptance test: a
// device that ACKS a rotate-cert as succeeded must have its cert state advanced
// so it LEAVES the near-expiry candidate set — the L4 convergence loop closes,
// and the producer stops re-emitting.
//
// It wires the full回执 path component-by-component (no real relay/bus, same
// relay-sim style as TestCertRenewal_F1): producer → queue → device dequeue →
// device ack (fires the completion hook) → rotation-resolved event → completion
// consumer advances cert state → next tick emits nothing.
func TestCertRenewal_CompletionClosesLoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Threshold 30d, CertValidity 90d (> threshold): a renewed cert is far from
	// expiry, so the device drops out of the candidate set. This mirrors the
	// cell's live co-tuning that buildCertRenewalSweeper asserts at startup.

	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(base)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Device with a near-expiry cert (epoch 1, expires in 24h < 30d threshold).
	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID: "dev-online", Name: "online-sensor", Status: "online", LastSeen: base,
		CertEpoch: 1, CertExpiresAt: base.Add(certNearExpiry24h),
	}))

	// Producer: cert-renewal reconciler emitting into rec.
	rec := outboxtest.NewRecorder()
	reconciler, err := devicecertrenewal.NewReconciler(fc, repo, rec.CellEmitter(),
		devicecertrenewal.Policy{Threshold: certRenewalThreshold, AttemptTTL: certRenewalAttemptTTL}, logger)
	require.NoError(t, err)

	// Completion consumer: publishes rotation-resolved into rec2 and advances cert
	// state on the subscribe side. Same repo as the producer (intra-cell).
	rec2 := outboxtest.NewRecorder()
	completionSvc, err := devicecertcompletion.NewService(fc, repo,
		devicecertcompletion.WithEmitter(rec2.CellEmitter()))
	require.NoError(t, err)

	// Consumer queue + devicecmd.Service WITH the completion hook wired (as the
	// cell composition root wires it into the public ack-serving Service).
	queue := commandtest.NewInMemQueue()
	queue.Now = fc.Now
	codec := newTestCursorCodec(t)
	svc, err := devicecmd.NewService(fc, queue, repo, codec, logger, query.RunModeForDemo(true),
		devicecmd.WithSliceName("devicecommand"),
		devicecmd.WithOnCommandResolved(completionSvc.OnCommandResolved))
	require.NoError(t, err)
	handler := slicecmd.EnqueueCommandAdapter{S: svc}

	// --- Tick 1: produce + dispatch the rotate-cert command. ---
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Len(t, rec.Entries(), 1, "tick 1 must emit exactly 1 rotate-cert")
	dispatchNew(t, ctx, handler, rec)

	active, err := queue.ScanActive(ctx, kcommand.ScanFilter{DeviceID: "dev-online"})
	require.NoError(t, err)
	require.Len(t, active, 1)
	cmdID := active[0].ID

	// --- Device executes: dequeue (→ Sent) then ack success 1h later. ---
	_, err = queue.Dequeue(ctx, "dev-online", 1, kcommand.DefaultLeaseDuration)
	require.NoError(t, err)
	fc.Advance(time.Hour)
	ackAt := fc.Now()
	// The device acks its OWN rotate-cert — present a device-self principal so the
	// completion hook recognizes this as a genuine device observation and emits
	// (operator/admin acks carry a different subject and would NOT advance state).
	require.NoError(t, svc.Ack(deviceSelfCtx(ctx, "dev-online"), "dev-online", cmdID, kcommand.AckSuccess))

	// --- The ack hook emitted a rotation-resolved event; relay-sim it to the consumer. ---
	resolved := rec2.Entries()
	require.Len(t, resolved, 1, "ack of a rotate-cert must emit exactly 1 rotation-resolved event")
	res := completionSvc.HandleRotationResolved(ctx, resolved[0])
	require.Equal(t, outbox.DispositionAck, res.Disposition, "consumer must Ack the resolved event")

	// --- Loop-closing assertions: cert state advanced. ---
	got, err := repo.GetByID(ctx, "dev-online")
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.CertEpoch, "epoch advanced 1→2 on rotation completion")
	assert.True(t, got.CertExpiresAt.Equal(ackAt.Add(domain.CertValidity)),
		"cert expiry advanced to ackAt + CertValidity (got %v)", got.CertExpiresAt)

	// --- The headline: next tick must NOT re-emit (device left the candidate set). ---
	rec.Reset()
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Empty(t, rec.Entries(),
		"a renewed device must NOT be re-emitted — the L4 convergence loop has closed (#1870)")
}

// e2eFailingEmitter is an outbox.Emitter that always returns an error from Emit.
// Used to simulate a failing event bus in the self-heal e2e test.
type e2eFailingEmitter struct{ err error }

func (f e2eFailingEmitter) Emit(_ context.Context, _ outbox.Entry) error { return f.err }

// TestCertRenewal_EmitFailureSelfHeals proves that when the OnCommandResolved hook
// fails to emit the rotation-resolved event (e.g. bus unavailable), the cert state
// is NOT advanced (no rotation-resolved event reaches the consumer) and the next
// reconcile tick STILL emits a fresh rotate-cert — proving the level-triggered
// reconcile loop self-heals without any producer-side state.
//
// Flow:
//  1. Tick 1: reconciler emits rotate-cert → dispatched to queue.
//  2. Device dequeues and acks success — OnCommandResolved fires with a FAILING
//     emitter, so the rotation-resolved event is silently dropped (logged only).
//  3. No rotation-resolved event reaches the completion consumer → cert state stays
//     at epoch 1 / near-expiry.
//  4. Tick 2: the device is STILL a near-expiry candidate → reconciler emits again,
//     proving the loop drives a fresh rotate-cert (the self-heal).
func TestCertRenewal_EmitFailureSelfHeals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(base)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Device with a near-expiry cert (epoch 1, expires in 24h < 30d threshold).
	repo := mem.NewDeviceRepository()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID: "dev-failemit", Name: "fail-emit-sensor", Status: "online", LastSeen: base,
		CertEpoch: 1, CertExpiresAt: base.Add(certNearExpiry24h),
	}))

	// Producer: cert-renewal reconciler emitting into rec.
	rec := outboxtest.NewRecorder()
	reconciler, err := devicecertrenewal.NewReconciler(fc, repo, rec.CellEmitter(),
		devicecertrenewal.Policy{Threshold: certRenewalThreshold, AttemptTTL: certRenewalAttemptTTL}, logger)
	require.NoError(t, err)

	// Completion service with a FAILING emitter — Emit always errors.
	// This means the rotation-resolved event is never delivered to the consumer.
	failEmit := e2eFailingEmitter{err: errors.New("bus unavailable")}
	completionSvc, err := devicecertcompletion.NewService(fc, repo,
		devicecertcompletion.WithEmitter(outbox.WrapEmitterForCell(failEmit)))
	require.NoError(t, err)

	// Command queue + devicecmd.Service with the failing completion hook wired.
	queue := commandtest.NewInMemQueue()
	queue.Now = fc.Now
	codec := newTestCursorCodec(t)
	svc, err := devicecmd.NewService(fc, queue, repo, codec, logger, query.RunModeForDemo(true),
		devicecmd.WithSliceName("devicecommand"),
		devicecmd.WithOnCommandResolved(completionSvc.OnCommandResolved))
	require.NoError(t, err)
	handler := slicecmd.EnqueueCommandAdapter{S: svc}

	// --- Tick 1: produce + dispatch the rotate-cert command. ---
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Len(t, rec.Entries(), 1, "tick 1 must emit exactly 1 rotate-cert")
	dispatchNew(t, ctx, handler, rec)

	active, err := queue.ScanActive(ctx, kcommand.ScanFilter{DeviceID: "dev-failemit"})
	require.NoError(t, err)
	require.Len(t, active, 1, "one active rotate-cert after tick 1")
	cmdID := active[0].ID

	// --- Device dequeues and acks success — emit FAILS silently. ---
	_, err = queue.Dequeue(ctx, "dev-failemit", 1, kcommand.DefaultLeaseDuration)
	require.NoError(t, err)
	fc.Advance(time.Hour)
	require.NoError(t, svc.Ack(deviceSelfCtx(ctx, "dev-failemit"), "dev-failemit", cmdID, kcommand.AckSuccess))
	// The hook fired, but Emit returned an error — it was logged and swallowed.
	// No rotation-resolved event was delivered to the consumer.

	// --- Assert: cert state NOT advanced (still epoch 1 / near-expiry). ---
	got, err := repo.GetByID(ctx, "dev-failemit")
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.CertEpoch,
		"cert state must NOT be advanced when emit fails (no resolved event delivered)")
	assert.True(t, got.CertExpiresAt.Equal(base.Add(certNearExpiry24h)),
		"cert expiry must be unchanged when emit fails")

	// --- Tick 2: device is still near-expiry → reconciler must re-emit. ---
	// The active-uniqueness key was released when the command was acked (terminal).
	// The next tick can enqueue a FRESH rotate-cert, proving the level-triggered self-heal.
	rec.Reset()
	_, err = reconciler.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.NotEmpty(t, rec.Entries(),
		"level-triggered self-heal: near-expiry device must still be driven on next tick after emit failure")
}
