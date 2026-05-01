package wrapper

import (
	"context"
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Re-export outbox types so wrapper callers do not need to import kernel/outbox
// for these core types. This keeps the dependency graph shallow.
type (
	// Entry is an alias for outbox.Entry. Use wrapper.Entry in handler signatures.
	Entry = outbox.Entry
	// Disposition is an alias for outbox.Disposition.
	Disposition = outbox.Disposition
	// HandleResult is an alias for outbox.HandleResult.
	HandleResult = outbox.HandleResult
)

const (
	// DispositionAck re-exports outbox.DispositionAck.
	DispositionAck = outbox.DispositionAck
	// DispositionRequeue re-exports outbox.DispositionRequeue.
	DispositionRequeue = outbox.DispositionRequeue
	// DispositionReject re-exports outbox.DispositionReject.
	DispositionReject = outbox.DispositionReject
)

// ConsumerFunc mirrors outbox.EntryHandler so Cells can wrap their existing
// handlers without changing signatures. It is re-exported here so wrapper
// callers do not need to import kernel/outbox for the type.
type ConsumerFunc = outbox.EntryHandler

// ErrorRedactor transforms an error before it is recorded on a span. Return
// the original err unchanged to disable redaction for a given error; return
// a replacement error (with sanitized message) to strip sensitive payload
// fragments (SQL snippets, token carriers, raw PII, ...) from observability
// backends.
//
// The default redactor used by WrapConsumer / middleware.Tracing when none
// is supplied is the identity — error text reaches the span verbatim, which
// matches OTel's default semantic. Operators who need scrub rules either
// (a) inject a redactor via the relevant Option or bootstrap wiring, or
// (b) rely on the OTel collector / adapter pipeline to scrub at export
// time.
type ErrorRedactor func(error) error

// ConsumerOption configures WrapConsumer at registration time.
type ConsumerOption func(*consumerConfig)

type consumerConfig struct {
	redactor ErrorRedactor
}

// WithConsumerErrorRedactor installs an ErrorRedactor on this WrapConsumer
// invocation. A nil fn is a no-op (identity redaction remains).
func WithConsumerErrorRedactor(fn ErrorRedactor) ConsumerOption {
	return func(c *consumerConfig) {
		if fn != nil {
			c.redactor = fn
		}
	}
}

func defaultEventSpanName(spec ContractSpec) string {
	return "CONSUME " + spec.Topic
}

// WrapConsumer wraps fn with a traced span + contract-id context derivation.
// The wrapper:
//   - starts a span named "CONSUME {topic}" using the supplied tracer
//   - sets gocell.contract.id / kind / transport, messaging.system /
//     destination attrs
//   - SetStatus(Error) + RecordError for any Requeue / Reject disposition
//     (the disposition itself is the authoritative control-flow signal —
//     wrapper does not modify it)
//   - propagates contract id through the context passed to fn
//   - defers recoverAndFinish so a panic in fn ends the span before
//     re-panicking
//
// tr is the Tracer supplied by the runtime infrastructure (typically
// runtime/eventrouter.Router). A nil tr falls back to NoopTracer{} — spans
// are silently discarded rather than panicking.
//
// spec must have Kind == "event" and Topic set; fn must be non-nil.
//
// WithConsumerErrorRedactor allows callers to scrub error text before it
// reaches span.RecordError (F5 / round-4). Applied uniformly to the three
// RecordError call sites below (Requeue/Reject disposition + panic path).
//
// Returns a non-nil error when fn is nil, spec.Kind != "event", or
// spec.Validate fails. Callers that want to fail-fast at composition time
// should use MustWrapConsumer.
func WrapConsumer(tr Tracer, spec ContractSpec, fn ConsumerFunc, opts ...ConsumerOption) (ConsumerFunc, error) {
	if fn == nil {
		return nil, fmt.Errorf("wrapper.WrapConsumer: fn must not be nil")
	}
	if spec.Kind != "event" {
		return nil, fmt.Errorf("wrapper.WrapConsumer: spec.Kind %q must be \"event\"", spec.Kind)
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("wrapper.WrapConsumer: %w", err)
	}
	if tr == nil {
		tr = NoopTracer{}
	}

	cfg := consumerConfig{redactor: identityRedactor}
	for _, o := range opts {
		o(&cfg)
	}

	baseAttrs := []Attr{
		{Key: "gocell.contract.id", Value: spec.ID},
		{Key: "gocell.contract.kind", Value: spec.Kind},
		{Key: "gocell.contract.transport", Value: spec.Transport},
		{Key: "messaging.system", Value: spec.Transport},
		{Key: "messaging.destination", Value: spec.Topic},
	}

	return func(ctx context.Context, entry outbox.Entry) (res outbox.HandleResult) {
		ctx = ctxkeys.WithContractID(ctx, spec.ID)
		ctx, span := tr.Start(ctx, defaultEventSpanName(spec))
		span.SetAttributes(baseAttrs...)
		defer func() { recoverAndFinishWithRedactor(span, recover(), cfg.redactor) }()

		res = fn(ctx, entry)

		switch res.Disposition {
		case outbox.DispositionAck:
			span.SetStatus(StatusOK, "")
		case outbox.DispositionRequeue:
			span.SetStatus(StatusError, "requeue")
			recordErrRedacted(span, cfg.redactor, res.Err, "consumer returned Requeue without error")
		case outbox.DispositionReject:
			span.SetStatus(StatusError, "reject")
			recordErrRedacted(span, cfg.redactor, res.Err, "consumer returned Reject without error")
		default:
			// Invalid disposition — tested in kernel/outbox; we mark the
			// span as error AND record an error event so ops see both the
			// status and a recognizable cause in the span, matching the
			// Requeue/Reject branches. Result is left untouched for
			// downstream downgrade logic.
			span.SetStatus(StatusError, "invalid disposition")
			recordErrRedacted(span, cfg.redactor, res.Err,
				fmt.Sprintf("consumer returned invalid disposition %d", res.Disposition))
		}
		span.End()
		return res
	}, nil
}

// MustWrapConsumer is the composition-root fail-fast variant of WrapConsumer.
// It panics when WrapConsumer returns an error. Suitable for static wiring
// where the spec is a build-time literal; use WrapConsumer directly when the
// spec is data-driven.
func MustWrapConsumer(tr Tracer, spec ContractSpec, fn ConsumerFunc, opts ...ConsumerOption) ConsumerFunc {
	c, err := WrapConsumer(tr, spec, fn, opts...)
	if err != nil {
		panic(err.Error())
	}
	return c
}

// identityRedactor is the default — passes errors through unchanged.
func identityRedactor(err error) error { return err }

// recordErrRedacted calls span.RecordError on the redacted form of err,
// falling back to a synthetic "missing error" message (itself redacted) so
// ops still get a recognizable event in the span even when a handler
// misbehaves by returning a non-Ack disposition with a nil error.
func recordErrRedacted(span Span, redact ErrorRedactor, err error, fallbackMsg string) {
	if err == nil {
		err = errors.New(fallbackMsg)
	}
	span.RecordError(redact(err))
}
