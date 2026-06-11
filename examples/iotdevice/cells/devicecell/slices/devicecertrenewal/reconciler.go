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

// Policy holds tuning parameters for the cert-renewal Reconciler. All fields
// must be positive; NewReconciler validates them via Policy.validate().
type Policy struct {
	// Threshold is the near-expiry look-ahead window: a device whose certificate
	// expires within now+Threshold is a renewal candidate.
	Threshold time.Duration
	// RetryInterval is the re-eligibility window for an un-advanced epoch. After
	// a successful emit+mark for epoch E the cert is suppressed for RetryInterval;
	// once that elapses with the epoch un-advanced (device never executed /
	// rotate-cert terminal-failed / cert expired) the same (device, epoch) pair
	// re-enters the candidate set and re-emits the SAME commandID. The relay
	// Claimer (24h done-TTL) dedups same-window re-emits; RetryInterval is chosen
	// ≥ that TTL so a genuine retry re-dispatches once the prior done-key expires
	// (or immediately if the prior command terminal-failed and the relay released
	// its claim). See certRenewalRetryInterval in cell.go for the default value
	// and co-tuning rationale.
	RetryInterval time.Duration
	// BatchSize is the maximum number of renewal candidates scanned and emitted in
	// a single Reconcile call. When a full batch is returned, Reconcile returns
	// RequeueAfter=BatchRequeue so the Loop continues with the next batch promptly;
	// when fewer candidates than BatchSize are returned the Loop reverts to the
	// normal TickerTrigger cadence.
	BatchSize int
	// BatchRequeue is the delay before continuing to the next batch when a full
	// batch was returned. A small value (e.g. 1s) paces the drain without bursting
	// the outbox/relay.
	BatchRequeue time.Duration
}

// validate returns an error when any Policy field is non-positive.
func (p Policy) validate() error {
	if p.Threshold <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.Policy: Threshold must be positive")
	}
	if p.RetryInterval <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.Policy: RetryInterval must be positive")
	}
	if p.BatchSize <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.Policy: BatchSize must be positive")
	}
	if p.BatchRequeue <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.Policy: BatchRequeue must be positive")
	}
	return nil
}

// Reconciler is the cert-renewal producer: a reconcile.Reconciler that, on each
// tick, scans the device repository for near-expiry certificates and enqueues a
// rotate-cert async command per device via runtime/command.EmitAsync.
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757):
// it reuses the already-activated async-dispatch path (#1698 WithCommandDispatch
// Claimer wrap) and #1699 DeriveCommandKey — zero new dispatch funnel.
//
// # Per-epoch dedup and time-window retry release
//
// Per-epoch dedup is owned by the durable devices.renewal_requested_epoch and
// devices.renewal_requested_at columns (#1819, #1820). After a successful emit
// the Reconciler marks BOTH columns atomically (DeviceRepository.MarkCertRenewalRequested),
// so ListCertificateRenewalCandidates suppresses the cert for Policy.RetryInterval.
// After RetryInterval elapses with the epoch un-advanced (terminal-failed command /
// device never executed / cert expired), the same (device, epoch) pair re-enters
// the candidate set and the Reconciler re-emits the SAME commandID — the relay
// Claimer (24h done-TTL) dedups any same-window re-emit, and RetryInterval is
// chosen ≥ that TTL so a genuine retry re-dispatches once the prior done-key
// expires (or immediately if the prior command terminal-failed and the relay
// released its claim).
//
// This makes each un-renewed cert produce AT MOST ONE command per RetryInterval
// per epoch, until a post-rotation re-issue advances the epoch (which yields a
// fresh commandID that dispatches again unconditionally).
//
// # Batch boundary
//
// Each Reconcile scans and emits at most Policy.BatchSize candidates and returns
// RequeueAfter=Policy.BatchRequeue when the batch was full, so a large backlog
// drains across requeues without a single-tick emit burst. Already-marked certs
// fall outside the retryBefore window on subsequent scans (monotone progress).
//
// # Emit+mark atomicity
//
// The emit and the renewal mark commit atomically in one ambient transaction (see
// enqueueRenewal). A failed mark rolls the emit back, so the cert stays a candidate
// and the next tick retries (no orphan command, never a suppressed-but-never-sent
// renewal). A committed emit always carries its mark (no "emit committed, mark
// lost" window). The per-epoch, per-RetryInterval guarantee therefore holds
// end-to-end independently of the relay's 24h Claimer TTL, which is a pure
// defense-in-depth backstop for any same-window re-emit (e.g. concurrent scans
// before either commits).
//
// # SINGLE-TENANT ASSUMPTION
//
// A reconcile loop is a background control loop with no request principal. The
// reconcile framework positively installs a system producer identity at its single
// reconcile chokepoint (kernel/reconcile.Loop.process →
// installSystemProducerIdentity, #1821): actor/subject="system", tenant cleared.
// So the tenant dimension of the Claimer key resolves to the "_notenant" sentinel
// for every emitted command as a CODE FACT — not because the lifecycle ctx happens
// to be empty, and not changeable by an ambient principal leaking into that ctx
// (the install overwrites). The devices table is likewise not tenant-partitioned in
// this example. That is correct for this single-tenant iotdevice example (device
// ids are UUIDs), but anyone copying this archetype into a MULTI-TENANT cell MUST
// add a tenant dimension to both the devices scan/mark and the commandID
// derivation — the framework system identity is deliberately tenantless, so a
// multi-tenant reconciler cannot rely on an ambient ctx tenant; otherwise two
// tenants sharing a device id would collide on the same Claimer key and one
// tenant's renewal would suppress the other's.
type Reconciler struct {
	clk      clock.Clock
	repo     domain.DeviceRepository
	emitter  outbox.CellEmitter
	txRunner persistence.CellTxManager
	policy   Policy
	logger   *slog.Logger
}

// Compile-time proof the producer satisfies the reconcile.Reconciler contract.
var _ reconcile.Reconciler = (*Reconciler)(nil)

// NewReconciler constructs the cert-renewal Reconciler. clk is a mandatory
// positional dependency; repo, emitter and txRunner are required and fail fast
// when nil; policy fields must all be positive (validated by Policy.validate());
// a nil logger falls back to slog.Default().
func NewReconciler(
	clk clock.Clock,
	repo domain.DeviceRepository,
	emitter outbox.CellEmitter,
	txRunner persistence.CellTxManager,
	policy Policy,
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
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		clk: clk, repo: repo, emitter: emitter, txRunner: txRunner,
		policy: policy, logger: logger,
	}, nil
}

// Reconcile observes every near-expiry certificate and enqueues a renewal command
// for each. req.EntityID is ignored: the loop is driven by a TickerTrigger whose
// resync-all sentinel (empty EntityID) means "re-observe every cert you own" — the
// renewal producer always sweeps the full near-expiry set (mirroring the device
// command sweeper).
//
// When a full batch (len==BatchSize) is returned, Reconcile returns
// RequeueAfter=BatchRequeue to continue draining the next batch promptly. Already-
// marked certs drop out of the next scan (within the RetryInterval window), so
// progress is monotonic and the drain terminates.
func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	now := r.clk.Now()
	cutoff := now.Add(r.policy.Threshold)
	retryBefore := now.Add(-r.policy.RetryInterval)
	candidates, err := r.repo.ListCertificateRenewalCandidates(ctx, cutoff, retryBefore, r.policy.BatchSize)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("devicecertrenewal: scan near-expiry: %w", err)
	}
	for _, cand := range candidates {
		if err := r.enqueueRenewal(ctx, cand, now); err != nil {
			// Transient: bubble up so the Loop applies backoff and re-sweeps on the
			// next tick. Already-requested epochs are skipped by the scan on retry,
			// and the Claimer dedups any same-window re-emit.
			// Note: a single bad candidate aborts the batch (transient bubble →
			// backoff retry); a future skip-bad-entry-continue would branch on
			// reconcile.IsPermanent here.
			return reconcile.Result{}, err
		}
	}
	if len(candidates) > 0 {
		r.logger.Info("devicecertrenewal: swept cert-renewal batch",
			slog.Int("emitted", len(candidates)),
			slog.Bool("full_batch", len(candidates) == r.policy.BatchSize))
	}
	if len(candidates) == r.policy.BatchSize {
		// Full batch: more candidates may remain — requeue promptly to drain the
		// next batch. Marked certs drop out of the next scan (time-window), so
		// progress is monotonic.
		return reconcile.Result{RequeueAfter: r.policy.BatchRequeue}, nil
	}
	return reconcile.Result{}, nil
}

// enqueueRenewal emits one rotate-cert async command for a near-expiry cert and
// marks the cert's epoch renewal-requested (with requestedAt=now) so later ticks
// within RetryInterval skip it. BOTH writes run inside a SINGLE txRunner.RunInTx:
// in durable PG mode the outbox writer and the devices UPDATE both route through
// the same ambient pgx.Tx (pgexec.PGExecutor reads persistence.TxFromContext), so
// they commit or roll back atomically. That closes the single-emit gap end to end —
// a failed mark rolls the emit back, so the cert stays a candidate and the next
// tick retries (no orphan command, never a suppressed-but-never-sent renewal), and
// a committed emit always carries its mark (no "emit committed, mark lost" window).
// Demo mode wires the no-op DemoCellTxManager: writes are not transactional there,
// which is acceptable because demo mode makes no durability guarantee (a stray
// re-emit is Claimer-deduped).
func (r *Reconciler) enqueueRenewal(ctx context.Context, cand domain.CertificateRenewalCandidate, now time.Time) error {
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
		return r.repo.MarkCertRenewalRequested(txCtx, cand.DeviceID, cand.CertEpoch, now)
	}); err != nil {
		return fmt.Errorf("devicecertrenewal: enqueue cert-renewal command: %w", err)
	}
	r.logger.Debug("devicecertrenewal: enqueued cert-renewal command",
		slog.String("device_id", cand.DeviceID),
		slog.Int64("cert_epoch", cand.CertEpoch),
		slog.Time("not_after", cand.CertExpiresAt))
	return nil
}

// rotateCommandID is the SINGLE source for the cert-rotation dedup token. The
// relay derives the Claimer key from (tenant, subject=deviceID, commandID), so a
// stable id per (deviceID, epoch) makes the secondary Claimer backstop dedup any
// same-window re-emit of one cert epoch within the relay's 24h done-TTL window,
// while a post-rotation re-issue (new epoch) yields a fresh id that dispatches
// again. The authoritative per-epoch dedup is the devices.renewal_requested_at
// time-window (see the Reconciler doc): after the retryBefore window elapses the
// same commandID is re-emitted — the relay dedups it within the same 24h done-TTL
// window, and re-dispatches once the prior done-key expires (or immediately if the
// prior command terminal-failed and the relay released its claim). Callers MUST NOT
// hand-roll this string.
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
