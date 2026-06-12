package devicecertcompletion

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	rotationresolved "github.com/ghbvf/gocell/generated/contracts/event/devicecert-rotation-resolved/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// topicRotationResolved is the canonical event topic for cert rotation
// completion events (mirrors deviceregister.TopicDeviceRegistered). It must
// equal the contract id / spec topic of event.devicecert-rotation-resolved.v1.
const topicRotationResolved = "event.devicecert-rotation-resolved.v1"

// Outcome values mapped from command.AckReason and carried on the resolved
// event payload. Kept as a closed set; HandleRotationResolved rejects any value
// outside it as a permanent producer-side violation.
const (
	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
	outcomeRejected  = "rejected"
)

// Service is the cert-rotation completion slice service. It both PUBLISHES the
// rotation-resolved event (OnCommandResolved, wired into the devicecmd ack path)
// and SUBSCRIBES to it (HandleRotationResolved, the cert-state writer). See the
// package doc for the cert-manager two-controller rationale.
type Service struct {
	repo    domain.DeviceRepository `gocell:"required"`
	emitter outbox.CellEmitter
	clk     clock.Clock
	logger  *slog.Logger
}

// Option configures a devicecertcompletion Service.
type Option func(*Service)

// WithEmitter sets the event emitter. Accepts outbox.CellEmitter (sealed
// marker); callers in _test.go may use outbox.WrapEmitterForCell(e).
// Accumulative builder semantics: a nil emitter leaves the prior value in place.
func WithEmitter(e outbox.CellEmitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithLogger sets the structured logger. A nil logger is silently ignored,
// leaving the default in place.
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// NewService creates a devicecertcompletion Service. clk is a mandatory
// positional dependency; repo is required (the consumer cannot advance cert
// state without it) and fails fast when nil; the emitter defaults to a demo
// emitter and the logger to slog.Default().
func NewService(clk clock.Clock, repo domain.DeviceRepository, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "devicecertcompletion.NewService")
	s := &Service{
		repo:    repo,
		emitter: outbox.DemoCellEmitter(),
		clk:     clk,
		logger:  slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// rotateCertCmdPayload is this slice's file-local decode view of the rotate-cert
// command payload (DTO scope-A): only the epoch field is consumed here. It is
// deliberately NOT a shared Go type with the producer's payload struct — both
// align to the same wire shape, not to each other (cell-patterns DTO rule). The
// notAfter field the producer also writes is ignored: the new expiry is computed
// server-side as resolvedAt + domain.CertValidity (the example has no external CA).
type rotateCertCmdPayload struct {
	Epoch int64 `json:"epoch"`
}

// OnCommandResolved is the generic command-resolution hook the devicecmd Service
// fires after a terminal ack. For rotate-cert commands it emits a
// rotation-resolved event so the (async, idempotent) consumer can advance the
// device's observed cert state; every other command type is ignored.
//
// Emit failures are logged but NOT propagated: the device's ack already
// succeeded, and the reconcile loop self-heals — a device whose cert state was
// not advanced stays a near-expiry candidate and is re-driven on the next tick.
// Failing the ack here would be both wrong (the device DID ack) and pointless
// (the level-triggered loop already recovers).
func (s *Service) OnCommandResolved(ctx context.Context, entry command.Entry, reason command.AckReason) {
	if entry.CommandType != domain.RotateCertCommandType {
		return
	}
	outcome := outcomeFromReason(reason)
	if outcome == "" {
		// Unreachable: devicecmd.Ack validates reason before firing the hook.
		// Guard anyway so a malformed outcome never reaches the wire.
		s.logger.Error("devicecertcompletion: unknown ack reason; not emitting resolved event",
			slog.String("device_id", entry.DeviceID), slog.String("command_id", entry.ID),
			slog.String("reason", reason.String()))
		return
	}
	var cmd rotateCertCmdPayload
	if err := json.Unmarshal(entry.Payload, &cmd); err != nil {
		s.logger.Error("devicecertcompletion: rotate-cert command payload undecodable; not emitting resolved event",
			slog.String("device_id", entry.DeviceID), slog.String("command_id", entry.ID),
			slog.Any("error", err))
		return
	}

	payload, err := json.Marshal(rotationresolved.Payload{
		DeviceID:   entry.DeviceID,
		Epoch:      cmd.Epoch,
		Outcome:    outcome,
		ResolvedAt: s.clk.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		s.logger.Error("devicecertcompletion: marshal rotation-resolved payload failed",
			slog.String("device_id", entry.DeviceID), slog.Any("error", err))
		return
	}
	ev, err := outbox.NewEntry(s.clk, ctx, topicRotationResolved, payload)
	if err != nil {
		s.logger.Error("devicecertcompletion: build rotation-resolved entry failed",
			slog.String("device_id", entry.DeviceID), slog.Any("error", err))
		return
	}
	if err := s.emitter.Emit(ctx, ev); err != nil {
		s.logger.Error("devicecertcompletion: emit rotation-resolved failed (reconcile loop self-heals)",
			slog.String("device_id", entry.DeviceID), slog.Int64("epoch", cmd.Epoch),
			slog.String("outcome", outcome), slog.Any("error", err))
		return
	}
	s.logger.Debug("devicecertcompletion: emitted rotation-resolved",
		slog.String("device_id", entry.DeviceID), slog.Int64("epoch", cmd.Epoch),
		slog.String("outcome", outcome))
}

// HandleRotationResolved consumes event.devicecert-rotation-resolved.v1. On a
// succeeded outcome it CAS-advances the device's cert state (loop-closing write);
// on failed/rejected it records a structured warning; an unknown outcome or
// undecodable payload is a permanent producer-side violation routed to DLX.
//
// Idempotency: repo.AdvanceCertAfterRotation is a CAS on the rotated epoch, so a
// redelivered event (at-least-once) or a duplicate device ack is absorbed to a
// no-op. A transient repo error is requeued for backoff retry.
func (s *Service) HandleRotationResolved(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	var p rotationresolved.Payload
	if err := json.Unmarshal(entry.Payload(), &p); err != nil {
		s.logger.Error("devicecertcompletion: undecodable rotation-resolved payload, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID()))
		return outbox.Reject(outbox.NewPermanentError(
			fmt.Errorf("devicecertcompletion: unmarshal rotation-resolved: %w", err)))
	}
	if perm := validateResolvedPayload(p); perm != nil {
		s.logger.Error("devicecertcompletion: invalid rotation-resolved payload, routing to dead letter",
			slog.String("entry_id", entry.ID()), slog.Any("error", perm))
		return outbox.Reject(outbox.NewPermanentError(perm))
	}
	resolvedAt, err := time.Parse(time.RFC3339Nano, p.ResolvedAt)
	if err != nil {
		s.logger.Error("devicecertcompletion: unparseable resolvedAt, routing to dead letter",
			slog.String("entry_id", entry.ID()), slog.String("resolved_at", p.ResolvedAt),
			slog.Any("error", err))
		return outbox.Reject(outbox.NewPermanentError(
			fmt.Errorf("devicecertcompletion: parse resolvedAt %q: %w", p.ResolvedAt, err)))
	}

	switch p.Outcome {
	case outcomeSucceeded:
		return s.applySuccess(ctx, p, resolvedAt)
	case outcomeFailed, outcomeRejected:
		// Failure observability (回执-driven): a device explicitly reported the
		// rotation did not succeed. A structured, fail-closed-redacted warning is
		// the example's signal bar; a production deployment would also increment a
		// bounded failure counter (closed label set). No cert-state change — the
		// queue active-uniqueness already released the key, so the reconciler
		// re-drives a fresh attempt on the next tick.
		s.logger.Warn("devicecertcompletion: device reported rotate-cert did not succeed",
			slog.String("device_id", p.DeviceID), slog.Int64("epoch", p.Epoch),
			slog.String("outcome", p.Outcome))
		return outbox.Ack()
	default:
		s.logger.Error("devicecertcompletion: unknown outcome, routing to dead letter",
			slog.String("entry_id", entry.ID()), slog.String("outcome", p.Outcome))
		return outbox.Reject(outbox.NewPermanentError(
			fmt.Errorf("devicecertcompletion: unknown outcome %q", p.Outcome)))
	}
}

// applySuccess CAS-advances the device's cert state for a succeeded rotation.
func (s *Service) applySuccess(ctx context.Context, p rotationresolved.Payload, resolvedAt time.Time) outbox.HandleResult {
	newExpiry := resolvedAt.Add(domain.CertValidity)
	advanced, err := s.repo.AdvanceCertAfterRotation(ctx, p.DeviceID, p.Epoch, newExpiry)
	if err != nil {
		s.logger.Error("devicecertcompletion: advance cert state failed; requeueing",
			slog.String("device_id", p.DeviceID), slog.Int64("epoch", p.Epoch), slog.Any("error", err))
		return outbox.Requeue(fmt.Errorf("devicecertcompletion: advance cert state: %w", err))
	}
	if advanced {
		s.logger.Info("devicecertcompletion: cert rotation completed; advanced device cert state",
			slog.String("device_id", p.DeviceID), slog.Int64("rotated_epoch", p.Epoch),
			slog.Time("new_expiry", newExpiry))
	} else {
		// Idempotent no-op: stale epoch (already advanced) or device gone. Safe to Ack.
		s.logger.Debug("devicecertcompletion: rotation-resolved no-op (stale epoch or unknown device)",
			slog.String("device_id", p.DeviceID), slog.Int64("epoch", p.Epoch))
	}
	return outbox.Ack()
}

// outcomeFromReason maps a terminal command.AckReason to the resolved-event
// outcome value. Returns "" for an invalid reason (unreachable: Ack validates).
func outcomeFromReason(r command.AckReason) string {
	switch r {
	case command.AckSuccess:
		return outcomeSucceeded
	case command.AckFailed:
		return outcomeFailed
	case command.AckRejected:
		return outcomeRejected
	default:
		return ""
	}
}

// validateResolvedPayload checks the schema-required fields this consumer relies
// on. A violation is a permanent producer-side error (returned non-nil).
func validateResolvedPayload(p rotationresolved.Payload) error {
	if p.DeviceID == "" {
		return fmt.Errorf("rotation-resolved payload deviceId is empty")
	}
	if p.Epoch < domain.DefaultCertEpoch {
		return fmt.Errorf("rotation-resolved payload epoch %d < %d", p.Epoch, domain.DefaultCertEpoch)
	}
	if p.ResolvedAt == "" {
		return fmt.Errorf("rotation-resolved payload resolvedAt is empty")
	}
	return nil
}
