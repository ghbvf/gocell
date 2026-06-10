package devicecertrenewal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
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
// tick, scans the device repository for near-expiry certificates and enqueues a
// deduplicated rotate-cert async command per device via runtime/command.EmitAsync.
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757):
// it reuses the already-activated async-dispatch path (#1698 WithCommandDispatch
// Claimer wrap) and #1699 DeriveCommandKey — zero new dispatch funnel.
//
// Per-epoch dedup is owned by the durable devices.renewal_requested_epoch column
// (#1819), not the relay's command-done TTL: after a successful emit the Reconciler
// marks the cert's epoch renewal-requested (DeviceRepository.MarkCertRenewalRequested),
// so ListCertificateRenewalCandidates skips it on every later tick until a
// post-rotation re-issue advances the epoch. Because that state lives on the
// devices row it survives a restart in durable (PG) mode. That makes a single
// un-renewed cert yield exactly one command across its whole multi-day near-expiry
// window (the idempotent forcing function). Because the emit and the renewal mark
// commit atomically in one ambient transaction (see enqueueRenewal), the relay's
// Claimer — keyed by (tenant, deviceID, commandID) with commandID from (deviceID,
// certEpoch) — is a pure defense-in-depth backstop: it dedups any same-window
// re-emit (e.g. concurrent scans before either commits) within the standard 24h
// idempotency TTL. The single-emit guarantee itself does NOT depend on it — there
// is no "emit committed but mark lost" gap to mop up.
//
// SINGLE-TENANT ASSUMPTION: a reconcile loop runs on the cell lifecycle context,
// which carries no request principal — so the tenant dimension of the Claimer key
// resolves to the "_notenant" sentinel for every emitted command. The devices
// table is likewise not tenant-partitioned in this example. That is correct for
// this single-tenant iotdevice example (device ids are UUIDs), but anyone copying
// this archetype into a MULTI-TENANT cell MUST add a tenant dimension to both the
// devices scan/mark and the commandID derivation — otherwise two tenants sharing a
// device id would collide on the same Claimer key and one tenant's renewal would
// suppress the other's.
type Reconciler struct {
	clk       clock.Clock
	repo      domain.DeviceRepository
	emitter   outbox.CellEmitter
	txRunner  persistence.CellTxManager
	threshold time.Duration
	logger    *slog.Logger
}

// Compile-time proof the producer satisfies the reconcile.Reconciler contract.
var _ reconcile.Reconciler = (*Reconciler)(nil)

// NewReconciler constructs the cert-renewal Reconciler. clk is a mandatory
// positional dependency; repo, emitter and txRunner are required and fail fast
// when nil; threshold (the near-expiry window) must be positive; a nil logger
// falls back to slog.Default().
func NewReconciler(
	clk clock.Clock,
	repo domain.DeviceRepository,
	emitter outbox.CellEmitter,
	txRunner persistence.CellTxManager,
	threshold time.Duration,
	logger *slog.Logger,
) (*Reconciler, error) {
	clock.MustHaveClock(clk, "devicecertrenewal.NewReconciler")
	if validation.IsNilInterface(repo) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.NewReconciler: repo must not be nil")
	}
	if validation.IsNilInterface(emitter) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.NewReconciler: emitter must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.NewReconciler: txRunner must not be nil")
	}
	if threshold <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.NewReconciler: threshold must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		clk: clk, repo: repo, emitter: emitter, txRunner: txRunner,
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
	candidates, err := r.repo.ListCertificateRenewalCandidates(ctx, cutoff)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("devicecertrenewal: scan near-expiry: %w", err)
	}
	for _, cand := range candidates {
		if err := r.enqueueRenewal(ctx, cand); err != nil {
			// Transient: bubble up so the Loop applies backoff and re-sweeps on the
			// next tick. Already-requested epochs are skipped by the scan on retry,
			// and the Claimer dedups any same-window re-emit.
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

// enqueueRenewal emits one rotate-cert async command for a near-expiry cert and
// marks the cert's epoch renewal-requested so later ticks skip it. BOTH writes run
// inside a SINGLE txRunner.RunInTx: in durable PG mode the outbox writer and the
// devices UPDATE both route through the same ambient pgx.Tx (pgexec.PGExecutor
// reads persistence.TxFromContext), so they commit or roll back atomically. That
// closes the single-emit gap end to end — a failed mark rolls the emit back, so the
// cert stays a candidate and the next tick retries (no orphan command, never a
// suppressed-but-never-sent renewal), and a committed emit always carries its mark
// (no "emit committed, mark lost" window). The atomicity is why the per-epoch
// single-emit owned by devices.renewal_requested_epoch holds for the whole window
// independently of the relay's 24h Claimer TTL. Demo mode wires the no-op
// DemoCellTxManager: writes are not transactional there, which is acceptable
// because demo mode makes no durability guarantee (a stray re-emit is Claimer-deduped).
func (r *Reconciler) enqueueRenewal(ctx context.Context, cand domain.CertificateRenewalCandidate) error {
	payload, err := rotatePayload(cand)
	if err != nil {
		return err
	}
	req := cmdenqueue.Request{
		DeviceID:    cand.DeviceID,
		CommandType: rotateCertCommandType,
		Payload:     payload,
	}
	commandID := rotateCommandID(cand.DeviceID, cand.CertEpoch)
	if err := r.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := command.EmitAsync(txCtx, r.clk, r.emitter, cmdenqueue.DispatchID,
			cand.DeviceID, commandID, req); err != nil {
			return err
		}
		return r.repo.MarkCertRenewalRequested(txCtx, cand.DeviceID, cand.CertEpoch)
	}); err != nil {
		return fmt.Errorf("devicecertrenewal: enqueue cert-renewal command: %w", err)
	}
	r.logger.Info("devicecertrenewal: enqueued cert-renewal command",
		slog.String("device_id", cand.DeviceID),
		slog.Int64("cert_epoch", cand.CertEpoch),
		slog.Time("not_after", cand.CertExpiresAt))
	return nil
}

// rotateCommandID is the SINGLE source for the cert-rotation dedup token. The
// relay derives the Claimer key from (tenant, subject=deviceID, commandID), so a
// stable id per (deviceID, epoch) makes the secondary Claimer backstop dedup any
// same-window re-emit of one cert epoch, while a post-rotation re-issue (new epoch)
// yields a fresh id that dispatches again. (The authoritative per-epoch dedup is
// the devices.renewal_requested_epoch mark — see the Reconciler doc.) Callers MUST
// NOT hand-roll this string.
//
// The id deliberately embeds deviceID even though the Claimer key already scopes
// by subject=deviceID: this keeps the token self-describing in logs/DLX and safe
// if ever read standalone. The redundancy never causes collisions — distinct
// (deviceID, epoch) pairs always map to distinct ids.
func rotateCommandID(deviceID string, epoch int64) string {
	return fmt.Sprintf("cert-rotate:%s:%d", deviceID, epoch)
}

// rotateCertPayload is the typed, defined shape of the rotate-cert command's
// opaque enqueue payload string.
type rotateCertPayload struct {
	Epoch    int64  `json:"epoch"`
	NotAfter string `json:"notAfter"`
}

func rotatePayload(cand domain.CertificateRenewalCandidate) (string, error) {
	b, err := json.Marshal(rotateCertPayload{
		Epoch:    cand.CertEpoch,
		NotAfter: cand.CertExpiresAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("devicecertrenewal: marshal rotate-cert payload: %w", err)
	}
	return string(b), nil
}
