package devicecertcompletion

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	rotationresolved "github.com/ghbvf/gocell/generated/contracts/event/devicecert-rotation-resolved/v1"
)

// topicRotationResolved is the canonical event topic for cert rotation
// completion events (mirrors deviceregister.TopicDeviceRegistered). It must
// equal the contract id / spec topic of event.devicecert-rotation-resolved.v1.
const topicRotationResolved = "event.devicecert-rotation-resolved.v1"

// metricRotationResolved is the counter name for rotation outcome observations.
const metricRotationResolved = "devicecert_rotation_resolved_total"

// Service is the cert-rotation completion slice service. It both PUBLISHES the
// rotation-resolved event (OnCommandResolved, wired into the devicecmd ack path)
// and SUBSCRIBES to it (HandleRotationResolved, the cert-state writer). See the
// package doc for the cert-manager two-controller rationale.
type Service struct {
	repo            domain.DeviceRepository `gocell:"required"`
	emitter         outbox.CellEmitter
	clk             clock.Clock
	logger          *slog.Logger
	metricsProvider metrics.Provider   // transient: used in NewService to register counter
	rotationCounter metrics.CounterVec // devicecert_rotation_resolved_total{outcome}; nil = nop
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

// WithMetricsProvider wires a metrics.Provider so the service can register
// devicecert_rotation_resolved_total{outcome}. Optional; defaults to
// metrics.NopProvider{} when not set (the counter is a safe no-op). A nil
// provider is silently ignored, leaving the prior value in place.
func WithMetricsProvider(mp metrics.Provider) Option {
	return func(s *Service) {
		if mp != nil {
			s.metricsProvider = mp
		}
	}
}

// NewService creates a devicecertcompletion Service. clk is a mandatory
// positional dependency; repo is required (the consumer cannot advance cert
// state without it) and fails fast when nil; the emitter defaults to a demo
// emitter and the logger to slog.Default(). The metrics provider is optional
// (defaults to metrics.NopProvider{}) — pass WithMetricsProvider to wire a
// real backend.
func NewService(clk clock.Clock, repo domain.DeviceRepository, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "devicecertcompletion.NewService")
	s := &Service{
		repo:            repo,
		emitter:         outbox.DemoCellEmitter(),
		clk:             clk,
		logger:          slog.Default(),
		metricsProvider: metrics.NopProvider{},
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	counter, err := s.metricsProvider.CounterVec(metrics.CounterOpts{
		Name:       metricRotationResolved,
		Help:       "Total rotation-resolved events processed by devicecertcompletion, by outcome (succeeded/failed/rejected).",
		LabelNames: []string{"outcome"},
	})
	if err != nil {
		return nil, fmt.Errorf("devicecertcompletion: register %s: %w", metricRotationResolved, err)
	}
	s.rotationCounter = counter
	s.metricsProvider = nil // provider no longer needed after registration
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
// Device-self proof: only the device acking its OWN rotate-cert is a genuine
// cert-rotation observation, so the completion event is emitted ONLY when the
// authenticated subject equals entry.DeviceID. The ack route is contract-derived
// owner-scoped (permission device:consume, resource: id; slices/devicecommand, #2486), so an
// operator/admin (via the PDP device:consume baseline) can ack any device's command — that override still resolves the
// command (releases the active-uniqueness key) but MUST NOT advance observed cert
// state. Treating an operator ack as completion would desync server-observed cert
// state from the device's actual (still-old, near-expiry) cert and silently drop
// the device from the re-drive set — violating the cert-manager invariant this
// slice mirrors (status is OBSERVED, not asserted). No device-token issuer exists
// yet, so a device is PrincipalUser with Subject==deviceID; the proof is subject
// identity, not principal kind. Fail-closed when the principal is absent (e.g. a
// server-side Sweeper timeout is not a device observation). AI-robust: Medium
// runtime guard (the subject==deviceID relation binds two runtime strings the
// generic hook signature cannot bind at type level).
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
	if p, ok := auth.FromContext(ctx); !ok || p.Subject != entry.DeviceID {
		ackSubject := ""
		if ok {
			ackSubject = p.Subject
		}
		s.logger.Warn("devicecertcompletion: rotate-cert ack not from device-self; not advancing cert state",
			slog.String("device_id", entry.DeviceID), slog.String("command_id", entry.ID),
			slog.String("ack_subject", ackSubject))
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
			slog.String("device_id", entry.DeviceID), slog.String("command_id", entry.ID),
			slog.Any("error", err))
		return
	}
	ev, err := outbox.NewEntry(s.clk, ctx, topicRotationResolved, payload)
	if err != nil {
		s.logger.Error("devicecertcompletion: build rotation-resolved entry failed",
			slog.String("device_id", entry.DeviceID), slog.String("command_id", entry.ID),
			slog.Any("error", err))
		return
	}
	if err := s.emitter.Emit(ctx, ev); err != nil {
		s.logger.Error("devicecertcompletion: emit rotation-resolved failed (reconcile loop self-heals)",
			slog.String("device_id", entry.DeviceID), slog.Int64("epoch", cmd.Epoch),
			slog.String("outcome", string(outcome)), slog.Any("error", err))
		return
	}
	s.logger.Debug("devicecertcompletion: emitted rotation-resolved",
		slog.String("device_id", entry.DeviceID), slog.Int64("epoch", cmd.Epoch),
		slog.String("outcome", string(outcome)))
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

	// The wire value is decoded into the typed PayloadOutcome but json does NOT
	// enforce enum membership, so the default arm stays the wire-trust guard: an
	// out-of-set producer value is a permanent violation routed to DLX.
	switch p.Outcome {
	case rotationresolved.PayloadOutcomeSucceeded:
		s.incOutcomeCounter(ctx, p.Outcome)
		return s.applySuccess(ctx, p, resolvedAt)
	case rotationresolved.PayloadOutcomeFailed, rotationresolved.PayloadOutcomeRejected:
		// Failure observability (回执-driven): a device explicitly reported the
		// rotation did not succeed. WARN is chosen over Info to raise signal-to-noise
		// for device-reported failures; device_id is a non-PII device identifier.
		// A bounded devicecert_rotation_resolved_total{outcome} counter (FIX 3)
		// carries the aggregate. No cert-state change — the queue active-uniqueness
		// already released the key, so the reconciler re-drives a fresh attempt on
		// the next tick.
		s.incOutcomeCounter(ctx, p.Outcome)
		s.logger.Warn("devicecertcompletion: device reported rotate-cert did not succeed",
			slog.String("entry_id", entry.ID()), slog.String("device_id", p.DeviceID),
			slog.Int64("epoch", p.Epoch), slog.String("outcome", string(p.Outcome)))
		return outbox.Ack()
	default:
		s.logger.Error("devicecertcompletion: unknown outcome, routing to dead letter",
			slog.String("entry_id", entry.ID()), slog.String("outcome", string(p.Outcome)))
		return outbox.Reject(outbox.NewPermanentError(
			fmt.Errorf("devicecertcompletion: unknown outcome %q", p.Outcome)))
	}
}

// applySuccess CAS-advances the device's cert state for a succeeded rotation.
// Security note: newExpiry is ALWAYS server-computed (resolvedAt + domain.CertValidity),
// never taken from the device ack payload — devices are not trusted to self-report
// their NotAfter.
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
			slog.String("device_id", p.DeviceID), slog.Int64("epoch", p.Epoch),
			slog.Time("new_expiry", newExpiry))
	} else {
		// Idempotent no-op: stale epoch (already advanced) or device gone. Safe to Ack.
		s.logger.Debug("devicecertcompletion: rotation-resolved no-op (stale epoch or unknown device)",
			slog.String("device_id", p.DeviceID), slog.Int64("epoch", p.Epoch))
	}
	return outbox.Ack()
}

// incOutcomeCounter increments the devicecert_rotation_resolved_total counter for
// the given outcome (one of the generated rotationresolved.PayloadOutcome* values
// — the closed set validated by HandleRotationResolved before this is called). The
// typed parameter keeps the metric label sourced from the schema enum; the string
// conversion to the label value is centralized here. A nil counter (failed
// registration or NopProvider) is a safe no-op.
func (s *Service) incOutcomeCounter(ctx context.Context, outcome rotationresolved.PayloadOutcome) {
	if s.rotationCounter == nil {
		return
	}
	s.rotationCounter.With(metrics.Labels{"outcome": string(outcome)}).Inc(ctx)
}

// outcomeFromReason maps a terminal command.AckReason to the resolved-event
// outcome value. Returns "" for an invalid reason (unreachable: Ack validates).
// The result is the generated typed enum, so a producer typo is a compile error.
func outcomeFromReason(r command.AckReason) rotationresolved.PayloadOutcome {
	switch r {
	case command.AckSuccess:
		return rotationresolved.PayloadOutcomeSucceeded
	case command.AckFailed:
		return rotationresolved.PayloadOutcomeFailed
	case command.AckRejected:
		return rotationresolved.PayloadOutcomeRejected
	default:
		return ""
	}
}

// Field-length upper bounds for the rotation-resolved event payload. These are
// defense-in-depth limits against log-injection and parse-DoS attacks; the
// underlying wire format is trusted to carry reasonable values but the async
// event boundary is treated as untrusted (permanent error → DLX on violation).
const (
	maxDeviceIDLen   = 128
	maxOutcomeLen    = 32
	maxResolvedAtLen = 64
)

// validateResolvedPayload checks the schema-required fields this consumer relies
// on. A violation is a permanent producer-side error (returned non-nil).
func validateResolvedPayload(p rotationresolved.Payload) error {
	if p.DeviceID == "" {
		return fmt.Errorf("rotation-resolved payload deviceId is empty")
	}
	if len(p.DeviceID) > maxDeviceIDLen {
		return fmt.Errorf("rotation-resolved payload deviceId length %d exceeds limit %d", len(p.DeviceID), maxDeviceIDLen)
	}
	if p.Epoch < domain.DefaultCertEpoch {
		return fmt.Errorf("rotation-resolved payload epoch %d < %d", p.Epoch, domain.DefaultCertEpoch)
	}
	if p.ResolvedAt == "" {
		return fmt.Errorf("rotation-resolved payload resolvedAt is empty")
	}
	if len(p.ResolvedAt) > maxResolvedAtLen {
		return fmt.Errorf("rotation-resolved payload resolvedAt length %d exceeds limit %d", len(p.ResolvedAt), maxResolvedAtLen)
	}
	// outcome length is checked here (before the switch) so an overlong value is
	// a permanent validation failure (DLX) rather than an unknown-outcome Reject.
	if len(p.Outcome) > maxOutcomeLen {
		return fmt.Errorf("rotation-resolved payload outcome length %d exceeds limit %d", len(p.Outcome), maxOutcomeLen)
	}
	return nil
}
