package wrapper

import (
	"context"
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
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
// Error redaction is hardcoded to pkg/redaction.RedactError before any
// span.RecordError call, so sensitive substrings (password=, token=,
// Authorization: Bearer …) never reach the trace backend. There is no
// caller-side opt-out — dev / test surfaces that need raw error text read
// it from slog structured fields instead.
// ref: hashicorp/vault audit/entry_formatter.go (log_raw=false default)
// ref: golang/go src/net/url/url.go URL.Redacted()
//
// Returns a non-nil error when fn is nil, spec.Kind != "event", or
// spec.Validate fails. Callers that want to fail-fast at composition time
// should use MustWrapConsumer.
func WrapConsumer(tr Tracer, spec ContractSpec, fn ConsumerFunc) (ConsumerFunc, error) {
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
		defer func() { recoverAndFinish(span, recover()) }()

		res = fn(ctx, entry)

		switch res.Disposition {
		case outbox.DispositionAck:
			span.SetStatus(StatusOK, "")
		case outbox.DispositionRequeue:
			span.SetStatus(StatusError, "requeue")
			recordErr(span, res.Err, "consumer returned Requeue without error")
		case outbox.DispositionReject:
			span.SetStatus(StatusError, "reject")
			recordErr(span, res.Err, "consumer returned Reject without error")
		default:
			// Invalid disposition — tested in kernel/outbox; we mark the
			// span as error AND record an error event so ops see both the
			// status and a recognizable cause in the span, matching the
			// Requeue/Reject branches. Result is left untouched for
			// downstream downgrade logic.
			span.SetStatus(StatusError, "invalid disposition")
			recordErr(span, res.Err,
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
func MustWrapConsumer(tr Tracer, spec ContractSpec, fn ConsumerFunc) ConsumerFunc {
	c, err := WrapConsumer(tr, spec, fn)
	if err != nil {
		panic(errcode.Assertion("wrapper: consumer: %v", err))
	}
	return c
}

// recordErr calls span.RecordError on the redacted form of err, falling back
// to a synthetic "missing error" message (itself redacted) so ops still get
// a recognizable event in the span even when a handler misbehaves by
// returning a non-Ack disposition with a nil error.
func recordErr(span Span, err error, fallbackMsg string) {
	if err == nil {
		err = errors.New(fallbackMsg)
	}
	span.RecordError(redaction.RedactError(err))
}
