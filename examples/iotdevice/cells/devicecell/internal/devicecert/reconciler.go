package devicecert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/command"
)

// rotateCertCommandType is the CommandType stamped on the enqueued device command
// that instructs the device to rotate (renew) its certificate. It rides inside the
// existing command.devicecommand.enqueue.v1 payload — no new contract.
const rotateCertCommandType = "rotate-cert"

// Reconciler is the cert-renewal producer: a reconcile.Reconciler that, on each
// tick, scans the cert Store for near-expiry certificates and enqueues a
// deduplicated rotate-cert async command per device via runtime/command.EmitAsync.
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757):
// it reuses the already-activated async-dispatch path (#1698 WithCommandDispatch
// Claimer wrap) and #1699 DeriveCommandKey — zero new dispatch funnel. The
// Claimer dedups by (tenant, deviceID, commandID); commandID is derived from
// (deviceID, certEpoch) so every tick within one cert epoch dedups to a single
// dispatched command (the idempotent forcing function), while a post-rotation
// re-issue (new epoch) becomes a fresh, dispatchable command.
type Reconciler struct {
	clk       clock.Clock
	store     *Store
	emitter   outbox.CellEmitter
	txRunner  persistence.CellTxManager
	threshold time.Duration
	logger    *slog.Logger
}

// Compile-time proof the producer satisfies the reconcile.Reconciler contract.
var _ reconcile.Reconciler = (*Reconciler)(nil)

// NewReconciler constructs the cert-renewal Reconciler. clk is a mandatory
// positional dependency; store, emitter and txRunner are required and fail fast
// when nil; threshold (the near-expiry window) must be positive; a nil logger
// falls back to slog.Default().
func NewReconciler(
	clk clock.Clock,
	store *Store,
	emitter outbox.CellEmitter,
	txRunner persistence.CellTxManager,
	threshold time.Duration,
	logger *slog.Logger,
) (*Reconciler, error) {
	clock.MustHaveClock(clk, "devicecert.NewReconciler")
	if store == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert.NewReconciler: store must not be nil")
	}
	if validation.IsNilInterface(emitter) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert.NewReconciler: emitter must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert.NewReconciler: txRunner must not be nil")
	}
	if threshold <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert.NewReconciler: threshold must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		clk: clk, store: store, emitter: emitter, txRunner: txRunner,
		threshold: threshold, logger: logger,
	}, nil
}

// Reconcile observes every near-expiry certificate and enqueues a renewal command
// for each. req.EntityID is ignored: the loop is driven by a TickerTrigger whose
// resync-all sentinel (empty EntityID) means "re-observe every cert you own" — the
// renewal producer always sweeps the full near-expiry set (mirroring the device
// command sweeper).
func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	cutoff := r.clk.Now().Add(r.threshold)
	states, err := r.store.ScanNearExpiry(ctx, cutoff)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("devicecert: scan near-expiry: %w", err)
	}
	for _, st := range states {
		if err := r.enqueueRenewal(ctx, st); err != nil {
			// Transient: bubble up so the Loop applies backoff and re-sweeps on the
			// next tick. The Claimer dedups the already-enqueued certs on retry.
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

// enqueueRenewal emits one rotate-cert async command for a near-expiry cert. The
// EmitAsync write is wrapped in txRunner.RunInTx so the durable PG outbox writer
// gets a tx in ctx; demo mode uses the no-op DemoCellTxManager.
func (r *Reconciler) enqueueRenewal(ctx context.Context, st CertState) error {
	payload, err := rotatePayload(st)
	if err != nil {
		return err
	}
	req := cmdenqueue.Request{
		DeviceID:    st.DeviceID,
		CommandType: rotateCertCommandType,
		Payload:     payload,
	}
	commandID := rotateCommandID(st.DeviceID, st.Epoch)
	if err := r.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		return command.EmitAsync(txCtx, r.clk, r.emitter, cmdenqueue.DispatchID,
			st.DeviceID, commandID, req)
	}); err != nil {
		return fmt.Errorf("devicecert: emit rotate-cert command: %w", err)
	}
	r.logger.Info("devicecert: enqueued cert-renewal command",
		slog.String("device_id", st.DeviceID),
		slog.Int64("cert_epoch", st.Epoch),
		slog.Time("not_after", st.NotAfter))
	return nil
}

// rotateCommandID is the SINGLE source for the cert-rotation dedup token. The
// relay derives the Claimer key from (tenant, subject=deviceID, commandID), so a
// stable id per (deviceID, epoch) makes every reconcile tick within one cert
// epoch dedup to a single dispatched command, while a post-rotation re-issue (new
// epoch) yields a fresh id that dispatches again. Callers MUST NOT hand-roll this
// string.
func rotateCommandID(deviceID string, epoch int64) string {
	return fmt.Sprintf("cert-rotate:%s:%d", deviceID, epoch)
}

// rotateCertPayload is the typed, defined shape of the rotate-cert command's
// opaque enqueue payload string.
type rotateCertPayload struct {
	Epoch    int64  `json:"epoch"`
	NotAfter string `json:"notAfter"`
}

func rotatePayload(st CertState) (string, error) {
	b, err := json.Marshal(rotateCertPayload{
		Epoch:    st.Epoch,
		NotAfter: st.NotAfter.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("devicecert: marshal rotate-cert payload: %w", err)
	}
	return string(b), nil
}
