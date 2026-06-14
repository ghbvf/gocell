package devicebootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/runtime/command"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
)

// bootstrapCommandType is the CommandType stamped on the auto-enqueued bootstrap
// command for a newly registered device.
const bootstrapCommandType = "bootstrap"

// bootstrapPayload is the (empty) JSON command payload for a bootstrap command.
const bootstrapPayload = "{}"

// deviceRegisteredEvent is the file-local decode view of the
// event.device-registered.v1 payload. Per the DTO scope-A rule it is private to
// this slice and is NOT shared with the producer slice's Go type — both align to
// the payload schema, not to each other.
type deviceRegisteredEvent struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
}

// Service is the event-reactive bootstrap producer.
//
// emitter is the sealed CellEmitter marker (composition root wraps a raw
// Emitter); it embeds outbox.Emitter, so it is passed directly to
// command.EmitAsync's kout.Emitter parameter. It is an optional dependency
// (defaults to outbox.DemoCellEmitter), matching the deviceregister slice — no
// gocell:"required" tag.
//
// txRunner wraps command.EmitAsync in a real transaction when the durable
// outbox writer requires one (adapters/postgres.OutboxWriter.Write calls
// persistence.TxFromContext[pgx.Tx] and returns ErrAdapterPGNoTx when no tx
// is present in ctx). Defaults to outbox.DemoCellTxManager() — a no-op that
// just invokes the closure, so demo mode and existing tests work unchanged.
// Not required (no gocell:"required" tag): DemoCellTxManager is the safe
// default for any assembly that does not wire a real PG pool.
type Service struct {
	emitter  outbox.CellEmitter
	txRunner persistence.CellTxManager
	clk      clock.Clock
	logger   *slog.Logger
}

// Option configures a devicebootstrap Service.
type Option func(*Service)

// WithEmitter sets the event/command emitter. Accepts outbox.CellEmitter (sealed
// marker); callers in _test.go may use outbox.WrapEmitterForCell(e). Accumulative
// builder semantics: a nil emitter leaves the previously-set value in place.
func WithEmitter(e outbox.CellEmitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the CellTxManager used to wrap command.EmitAsync in a
// transaction. Required for durable mode: the PG outbox writer calls
// persistence.TxFromContext[pgx.Tx] and returns ErrAdapterPGNoTx when no tx
// is in ctx. Demo mode and tests use the default outbox.DemoCellTxManager()
// no-op. Accumulative: a nil txRunner leaves the previously-set value in place.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
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

// NewService creates a device-bootstrap Service. The clock is a mandatory
// positional dependency; the emitter, txRunner and logger fall back to
// demo/default values when not injected.
func NewService(clk clock.Clock, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "devicebootstrap.NewService")
	s := &Service{
		emitter:  outbox.DemoCellEmitter(),
		txRunner: outbox.DemoCellTxManager(),
		clk:      clk,
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// HandleDeviceRegistered reacts to an event.device-registered.v1 event by
// emitting a command.devicecommand.enqueue.v1 async command for the new device.
//
// Consumer: cg-devicecell-device-registered
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false).
func (s *Service) HandleDeviceRegistered(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	var ev deviceRegisteredEvent
	if err := json.Unmarshal(entry.Payload(), &ev); err != nil {
		s.logger.Error("device-bootstrap: failed to unmarshal device-registered event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID()))
		return outbox.Reject(outbox.NewPermanentError(
			fmt.Errorf("devicebootstrap: unmarshal device-registered: %w", err)))
	}

	req := cmdenqueue.Request{
		DeviceID:    ev.ID,
		CommandType: bootstrapCommandType,
		Payload:     bootstrapPayload,
	}
	// command_id = source event entry.ID() — deterministic across redelivery, so
	// an at-least-once replay of the same device-registered event dedups to the
	// same bootstrap command instance. subject = deviceID.
	//
	// EmitAsync is wrapped in txRunner.RunInTx so durable mode (PG outbox writer)
	// gets a transaction in ctx — adapters/postgres.OutboxWriter.Write requires
	// persistence.TxFromContext[pgx.Tx] and returns ErrAdapterPGNoTx otherwise.
	// Demo mode uses outbox.DemoCellTxManager() (no-op), which just calls the
	// closure directly, so behavior is unchanged for tests and demo assemblies.
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		return command.EmitAsync(txCtx, s.clk, s.emitter, cmdenqueue.DispatchID,
			ev.ID, entry.ID(), req)
	}); err != nil {
		s.logger.Error("device-bootstrap: failed to emit enqueue command",
			slog.String("device_id", ev.ID), slog.String("entry_id", entry.ID()),
			slog.Any("error", err))
		return outbox.Requeue(fmt.Errorf("devicebootstrap: emit enqueue command: %w", err))
	}

	s.logger.Info("device-bootstrap: enqueued bootstrap command",
		slog.String("device_id", ev.ID), slog.String("entry_id", entry.ID()))
	return outbox.Ack()
}
