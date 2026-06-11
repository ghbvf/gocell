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
	// AttemptTTL is the single retry-granularity timer: how long one rotate-cert
	// command attempt stays active (non-terminal) in the queue before the Sweeper
	// expires it. After expiry the slot is released and the next reconcile tick
	// re-enqueues a fresh attempt. AttemptTTL must comfortably exceed a healthy
	// device's poll+execute round-trip (e.g. 36h for a device that polls daily).
	AttemptTTL time.Duration
}

// validate returns an error when any Policy field is non-positive.
func (p Policy) validate() error {
	if p.Threshold <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.Policy: Threshold must be positive")
	}
	if p.AttemptTTL <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecertrenewal.Policy: AttemptTTL must be positive")
	}
	return nil
}

// Reconciler is a STATELESS cert-renewal producer: a reconcile.Reconciler that,
// on each tick, scans ALL near-expiry certificates and enqueues a rotate-cert
// async command per device via runtime/command.EmitAsync with active-uniqueness.
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757):
// it reuses the already-activated async-dispatch path (#1698 WithCommandDispatch
// Claimer wrap) and #1699 DeriveCommandKey — zero new dispatch funnel.
//
// # Correctness: queue-owned active-uniqueness (F1 fix, #1820)
//
// "At most one active rotate-cert per (device,epoch)" is owned by the QUEUE via
// the active-uniqueness mechanism (command.WithActiveUniqueness). Each emit sets
// EnqueueOptions.IdempotencyKey from DispatchedUniqueness(ctx) so the queue
// admits at most one non-terminal command per key. Duplicate emits (concurrent
// ticks) are coalesced by the queue to a no-op — no additional command is created.
//
// Un-executed commands self-expire: the OverallDeadline is set to
// now+Policy.AttemptTTL. Once that elapses, the command Sweeper transitions the
// command to terminal (Expired), releasing the active-uniqueness key. The next
// reconcile tick then re-enqueues a fresh attempt — level-triggered retry without
// any producer-side state.
//
// This makes F1 (offline devices accumulating unbounded duplicate commands)
// structurally impossible: the queue rejects all duplicates while a holder is
// non-terminal, and the Sweeper guarantees eventual termination.
//
// # Full sweep per tick
//
// Each Reconcile scans and emits for ALL near-expiry certs in a single call and
// returns reconcile.Result{} (zero RequeueAfter). The queue active-uniqueness
// coalesces any duplicate emits across ticks to no-ops, so repeated sweeps are
// safe. The TickerTrigger cadence (certRenewalSweepInterval, default 12h) is the
// sole pacing mechanism.
//
// NOTE: emit-all-per-tick is the correct model for example-scale (IoT example).
// A fleet-scale deployment with millions of devices should add a scan-side
// "skip if an active command already exists for this (device,epoch)" filter
// (observe-then-decide) to avoid emitting into the queue for already-pending
// renewals. That optimisation does NOT affect correctness — queue active-uniqueness
// holds regardless — but avoids the O(N) emit+coalesce cost on large fleets.
// It is intentionally out of scope for this example; the boundary is documented
// here rather than as a TODO to defer.
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
// add a tenant dimension to both the devices scan and the commandID derivation —
// the framework system identity is deliberately tenantless, so a multi-tenant
// reconciler cannot rely on an ambient ctx tenant; otherwise two tenants sharing a
// device id would collide on the same Claimer key and one tenant's renewal would
// suppress the other's.
type Reconciler struct {
	clk     clock.Clock
	repo    domain.DeviceRepository
	emitter outbox.CellEmitter
	policy  Policy
	logger  *slog.Logger
}

// Compile-time proof the producer satisfies the reconcile.Reconciler contract.
var _ reconcile.Reconciler = (*Reconciler)(nil)

// NewReconciler constructs the cert-renewal Reconciler. clk is a mandatory
// positional dependency; repo and emitter are required and fail fast when nil;
// policy fields must all be positive (validated by Policy.validate());
// a nil logger falls back to slog.Default().
func NewReconciler(
	clk clock.Clock,
	repo domain.DeviceRepository,
	emitter outbox.CellEmitter,
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
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		clk: clk, repo: repo, emitter: emitter,
		policy: policy, logger: logger,
	}, nil
}

// Reconcile observes ALL near-expiry certificates and enqueues a renewal command
// for each. req.EntityID is ignored: the loop is driven by a TickerTrigger whose
// resync-all sentinel (empty EntityID) means "re-observe every cert you own" — the
// renewal producer always sweeps the full near-expiry set and returns Result{} (no
// RequeueAfter). The queue active-uniqueness coalesces duplicate emits to no-ops,
// so repeated sweeps across ticks are safe.
func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	now := r.clk.Now()
	cutoff := now.Add(r.policy.Threshold)
	candidates, err := r.repo.ListCertificateRenewalCandidates(ctx, cutoff)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("devicecertrenewal: scan near-expiry: %w", err)
	}
	for _, cand := range candidates {
		if err := r.enqueueRenewal(ctx, cand, now); err != nil {
			// Transient: bubble up so the Loop applies backoff and re-sweeps on the
			// next tick. The queue active-uniqueness coalesces any successful prior
			// emits for the same (device,epoch), so re-sweeping is safe.
			return reconcile.Result{}, err
		}
	}
	if len(candidates) > 0 {
		r.logger.Info("devicecertrenewal: swept cert-renewal batch",
			slog.Int("emitted", len(candidates)))
	}
	return reconcile.Result{}, nil
}

// enqueueRenewal emits one rotate-cert async command for a near-expiry cert with
// active-uniqueness enabled (command.WithActiveUniqueness). The queue enforces
// "at most one non-terminal rotate-cert per (device,epoch)" via the IdempotencyKey
// set from DispatchedUniqueness(ctx); duplicate emits are coalesced to a no-op.
// The OverallDeadline (now+AttemptTTL) terminal-guarantees the command so the
// Sweeper can expire it when the device is offline, releasing the slot for retry.
//
// No txRunner wrapping is required: the emitter is self-durable (the outbox Writer
// commits the entry atomically in its own internal logic). There is no second write
// to coordinate, so RunInTx would add no atomicity value here.
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
	deadline := now.Add(r.policy.AttemptTTL)
	if err := command.EmitAsync(ctx, r.clk, r.emitter, cmdenqueue.DispatchID,
		cand.DeviceID, commandID, req,
		command.WithActiveUniqueness(deadline)); err != nil {
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
// stable id per (deviceID, epoch) makes active-uniqueness dedup any concurrent
// re-emit of one cert epoch, while a post-rotation re-issue (new epoch) yields a
// fresh id that dispatches again. Callers MUST NOT hand-roll this string.
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
