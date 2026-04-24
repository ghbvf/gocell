package wrapper

import (
	"context"
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// ConsumerFunc mirrors outbox.EntryHandler so Cells can wrap their existing
// handlers without changing signatures. It is re-exported here so wrapper
// callers do not need to import kernel/outbox for the type.
type ConsumerFunc = outbox.EntryHandler

// WrapConsumer wraps fn with a traced span + contract-id context derivation.
// The wrapper:
//   - starts a span named "CONSUME {topic}" (customisable via
//     WithConsumerSpanNameFormatter)
//   - sets gocell.contract.id / kind / transport, messaging.system /
//     destination attrs
//   - SetStatus(Error) + RecordError for any Requeue / Reject disposition
//     (the disposition itself is the authoritative control-flow signal —
//     wrapper does not modify it)
//   - propagates contract id through the context passed to fn
//
// spec must have Kind == "event" and Topic set; fn must be non-nil.
func WrapConsumer(spec ContractSpec, fn ConsumerFunc, opts ...Option) ConsumerFunc {
	if fn == nil {
		panic("wrapper.WrapConsumer: fn must not be nil")
	}
	if spec.Kind != "event" {
		panic(fmt.Sprintf("wrapper.WrapConsumer: spec.Kind %q must be \"event\"", spec.Kind))
	}
	if err := spec.Validate(); err != nil {
		panic(err.Error())
	}

	cfg := defaults()
	for _, opt := range opts {
		opt(&cfg)
	}

	baseAttrs := []Attr{
		{Key: "gocell.contract.id", Value: spec.ID},
		{Key: "gocell.contract.kind", Value: spec.Kind},
		{Key: "gocell.contract.transport", Value: spec.Transport},
		{Key: "messaging.system", Value: spec.Transport},
		{Key: "messaging.destination", Value: spec.Topic},
	}

	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		ctx = ctxkeys.WithContractID(ctx, spec.ID)
		ctx, span := cfg.tracer.Start(ctx, cfg.eventNamer(spec))
		span.SetAttributes(baseAttrs...)

		res := fn(ctx, entry)

		switch res.Disposition {
		case outbox.DispositionAck:
			span.SetStatus(StatusOK, "")
		case outbox.DispositionRequeue:
			span.SetStatus(StatusError, "requeue")
			if res.Err != nil {
				span.RecordError(res.Err)
			} else {
				span.RecordError(errors.New("consumer returned Requeue without error"))
			}
		case outbox.DispositionReject:
			span.SetStatus(StatusError, "reject")
			if res.Err != nil {
				span.RecordError(res.Err)
			} else {
				span.RecordError(errors.New("consumer returned Reject without error"))
			}
		default:
			// Invalid disposition — tested in kernel/outbox; we mark the
			// span as error so ops see the misbehaviour but leave the
			// result untouched for downstream downgrade logic.
			span.SetStatus(StatusError, "invalid disposition")
		}
		span.End()
		return res
	}
}
