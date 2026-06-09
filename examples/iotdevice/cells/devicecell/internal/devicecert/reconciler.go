// Package devicecert turns near-expiry device certificate state into
// idempotent async rotate-cert commands.
package devicecert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
)

const (
	CommandTypeRotateCert = "rotate-cert"
	RenewalThreshold      = 7 * 24 * time.Hour
)

// DeviceRepository is the certificate renewal subset of domain.DeviceRepository.
type DeviceRepository interface {
	ListCertificateRenewalCandidates(ctx context.Context, expiresBefore time.Time) ([]domain.CertificateRenewalCandidate, error)
}

// Reconciler scans near-expiry device certs and emits rotate-cert enqueue
// commands through the existing command.devicecommand.enqueue.v1 async path.
type Reconciler struct {
	repo     DeviceRepository
	emitter  outbox.CellEmitter
	txRunner persistence.CellTxManager
	clk      clock.Clock
	logger   *slog.Logger
}

type Option func(*Reconciler)

func WithLogger(logger *slog.Logger) Option {
	return func(r *Reconciler) {
		if logger != nil {
			r.logger = logger
		}
	}
}

func NewReconciler(
	clk clock.Clock,
	repo DeviceRepository,
	emitter outbox.CellEmitter,
	txRunner persistence.CellTxManager,
	opts ...Option,
) (*Reconciler, error) {
	clock.MustHaveClock(clk, "devicecert.NewReconciler")
	r := &Reconciler{
		repo:     repo,
		emitter:  emitter,
		txRunner: txRunner,
		clk:      clk,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reconciler) Validate() error {
	if r == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert: reconciler is nil")
	}
	if validation.IsNilInterface(r.repo) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert: device repository is required")
	}
	if validation.IsNilInterface(r.emitter) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert: command emitter is required")
	}
	if validation.IsNilInterface(r.txRunner) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert: tx manager is required")
	}
	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	now := r.clk.Now()
	candidates, err := r.repo.ListCertificateRenewalCandidates(ctx, now.Add(RenewalThreshold))
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("devicecert: scan renewal candidates: %w", err)
	}
	for _, c := range candidates {
		if err := r.emitRotateCert(ctx, now, c); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *Reconciler) emitRotateCert(
	ctx context.Context, requestedAt time.Time, c domain.CertificateRenewalCandidate,
) error {
	payload, err := json.Marshal(rotateCertPayload{
		DeviceID:      c.DeviceID,
		CertEpoch:     c.CertEpoch,
		CertExpiresAt: c.CertExpiresAt,
		RequestedAt:   requestedAt,
	})
	if err != nil {
		return fmt.Errorf("devicecert: marshal rotate-cert payload: %w", err)
	}
	req := cmdenqueue.Request{
		DeviceID:    c.DeviceID,
		CommandType: CommandTypeRotateCert,
		Payload:     string(payload),
	}
	commandID := RenewalCommandID(c.DeviceID, c.CertEpoch)
	if err := r.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		return command.EmitAsync(txCtx, r.clk, r.emitter, cmdenqueue.DispatchID, c.DeviceID, commandID, req)
	}); err != nil {
		return fmt.Errorf("devicecert: emit rotate-cert command: %w", err)
	}
	r.logger.Info("devicecert: emitted rotate-cert command",
		slog.String("device_id", c.DeviceID),
		slog.Int64("cert_epoch", c.CertEpoch),
		slog.String("command_id", commandID))
	return nil
}

// RenewalCommandID deterministically maps one device certificate epoch to one
// async command instance, so repeated reconcile ticks hit the same Claimer key.
func RenewalCommandID(deviceID string, certEpoch int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", deviceID, certEpoch)))
	return "cert-renewal-" + hex.EncodeToString(sum[:16])
}

type rotateCertPayload struct {
	DeviceID      string    `json:"deviceId"`
	CertEpoch     int64     `json:"certEpoch"`
	CertExpiresAt time.Time `json:"certExpiresAt"`
	RequestedAt   time.Time `json:"requestedAt"`
}
