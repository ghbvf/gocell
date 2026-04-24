package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Emitter emits an outbox entry either by writing it to a durable outbox or by
// directly publishing its canonical wire envelope.
//
// Implementations may optionally satisfy DurabilityReporter to expose whether
// their sink is backed by durable (transactional outbox) storage. Callers that
// need to decide L2/L0 slice upgrades should use ReportDurable, which returns
// false for any Emitter that does not implement the optional interface.
type Emitter interface {
	Emit(ctx context.Context, entry Entry) error
}

// DirectPublishFailureMode controls direct publisher error handling.
type DirectPublishFailureMode int

const (
	DirectPublishFailClosed DirectPublishFailureMode = iota + 1
	DirectPublishFailOpen
)

// WriterEmitter emits by writing entries to the transactional outbox.
type WriterEmitter struct {
	writer Writer
}

// NewWriterEmitter adapts an outbox Writer into an Emitter.
func NewWriterEmitter(w Writer) (*WriterEmitter, error) {
	if isNilEmitterDependency(w) {
		return nil, errcode.New(errcode.ErrCellMissingOutbox,
			"outbox: nil writer for WriterEmitter")
	}
	return &WriterEmitter{writer: w}, nil
}

// NewNoopEmitter returns an Emitter backed by NoopWriter.
func NewNoopEmitter() Emitter {
	return &WriterEmitter{writer: NoopWriter{}}
}

func (e *WriterEmitter) Emit(ctx context.Context, entry Entry) error {
	if e == nil || isNilEmitterDependency(e.writer) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"outbox: nil writer for WriterEmitter")
	}
	return e.writer.Write(ctx, entry)
}

var _ Emitter = (*WriterEmitter)(nil)

// DirectEmitter emits by wrapping entries in the v1 wire envelope and calling
// Publisher.Publish directly.
type DirectEmitter struct {
	publisher Publisher
	mode      DirectPublishFailureMode
	logger    *slog.Logger
}

// NewDirectEmitter adapts a Publisher into an Emitter that publishes v1 wire
// envelopes. A nil logger uses slog.Default().
func NewDirectEmitter(p Publisher, mode DirectPublishFailureMode, loggers ...*slog.Logger) (*DirectEmitter, error) {
	if isNilEmitterDependency(p) {
		return nil, errcode.New(errcode.ErrCellMissingOutbox,
			"outbox: nil publisher for DirectEmitter")
	}
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &DirectEmitter{publisher: p, mode: mode, logger: logger}, nil
}

func (e *DirectEmitter) Emit(ctx context.Context, entry Entry) error {
	if e == nil || isNilEmitterDependency(e.publisher) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"outbox: nil publisher for DirectEmitter")
	}
	if err := entry.Validate(); err != nil {
		return err
	}
	envelope, err := MarshalEnvelope(entry)
	if err != nil {
		return err
	}
	topic := entry.RoutingTopic()
	if err := e.publisher.Publish(ctx, topic, envelope); err != nil {
		if e.mode == DirectPublishFailOpen {
			e.logger.Warn("outbox: direct publish failed (fail-open)",
				slog.String("topic", topic),
				slog.String("error", err.Error()))
			return nil
		}
		return fmt.Errorf("outbox: direct publish failed for topic %s: %w", topic, err)
	}
	return nil
}

var _ Emitter = (*DirectEmitter)(nil)

// DurabilityReporter is an optional interface Emitter implementations may
// expose so callers (typically Cell boundaries) can query whether this
// emitter is backed by durable (transactional outbox) sinks. Emitters that
// do not implement DurabilityReporter are treated as non-durable by callers
// — the safe default for direct-publish and noop paths.
//
// ref: kernel/cell.EmitterOutcome.Durable — the primary consumer; Cells
// use the reported value to decide whether optional slices (e.g. rbacassign)
// upgrade from L0 to L2.
// ref: github.com/ThreeDotsLabs/watermill message/router.go —
// `disabledPublisher` pattern; an explicit typed indicator lets callers
// branch on capability without runtime type switches.
type DurabilityReporter interface {
	Durable() bool
}

// Durable reports whether this WriterEmitter is backed by a real (non-noop)
// outbox.Writer. NoopWriter and any writer that advertises Noop()==true are
// considered non-durable; anything else is durable.
func (e *WriterEmitter) Durable() bool {
	if e == nil || e.writer == nil {
		return false
	}
	n, ok := e.writer.(interface{ Noop() bool })
	if !ok {
		return true
	}
	return !n.Noop()
}

// Durable always returns false for DirectEmitter: direct publishing bypasses
// the transactional outbox and therefore carries no durability guarantee by
// design.
func (*DirectEmitter) Durable() bool { return false }

// ReportDurable returns the durability status of an Emitter, defaulting to
// false when the implementation does not expose DurabilityReporter. Intended
// for Cell boundaries that accept a directly-injected Emitter via WithEmitter
// but still need to decide L2/L0 slice upgrades based on durability.
func ReportDurable(e Emitter) bool {
	if e == nil {
		return false
	}
	r, ok := e.(DurabilityReporter)
	return ok && r.Durable()
}

var (
	_ DurabilityReporter = (*WriterEmitter)(nil)
	_ DurabilityReporter = (*DirectEmitter)(nil)
)

func isNilEmitterDependency(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
