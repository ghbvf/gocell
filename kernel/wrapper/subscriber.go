package wrapper

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// WrapSubscriber wraps a SubscriberHandler with a contract delivery span whose
// status is resolved by the final broker settlement notification. It starts one
// span per delivery attempt, appends a SettlementObserver to the returned
// HandleResult, and ends the span when the subscriber calls outbox.NotifySettlement.
//
// This differs from WrapConsumer: Consumer middleware only sees the business
// HandleResult before Commit/Ack/Nack, while this wrapper observes the
// Subscriber layer after settlement downgrades such as commit_failed.
//
// WrapSubscriber relies on its caller (currently
// runtime/eventrouter.contractTracingSubscriber) to have validated the
// owning outbox.Subscription via Subscription.Validate before constructing
// the spec, and on registration-time spec validation in
// eventrouter.AddContractHandler. Both upstream layers run spec.Validate
// or its primitive subset; running it again here would be a redundant
// third pass without adding any safety margin (P2 #1 — single-source
// validation per Watermill / MassTransit pattern). Structural assertions
// (nil fn, kind!=event) remain because they catch programming errors
// that no upstream validate would have caught.
func WrapSubscriber(tr Tracer, spec contractspec.ContractSpec, fn outbox.SubscriberHandler) (outbox.SubscriberHandler, error) {
	if fn == nil {
		return nil, fmt.Errorf("wrapper.WrapSubscriber: fn must not be nil")
	}
	if spec.Kind != "event" {
		return nil, fmt.Errorf("wrapper.WrapSubscriber: spec.Kind %q must be \"event\"", spec.Kind)
	}
	if tr == nil {
		tr = NoopTracer{}
	}

	baseAttrs := []Attr{
		{Key: "gocell.contract.id", Value: spec.ID},
		{Key: "gocell.contract.kind", Value: string(spec.Kind)},
		{Key: "gocell.contract.transport", Value: spec.Transport},
		{Key: "messaging.system", Value: spec.Transport},
		{Key: "messaging.destination", Value: spec.Topic},
	}

	return func(ctx context.Context, entry outbox.Entry) (res outbox.HandleResult, settlement outbox.Settlement) {
		ctx = entry.Observability.RestoreToContext(ctx)
		ctx = ctxkeys.WithContractID(ctx, spec.ID)
		ctx, span := tr.Start(ctx, defaultEventSpanName(spec))
		span.SetAttributes(baseAttrs...)
		defer func() { recoverAndFinish(span, recover()) }()

		res, settlement = fn(ctx, entry)
		var once sync.Once
		res.SettlementObservers = append(res.SettlementObservers,
			outbox.SettlementObserverFunc(func(_ context.Context, obs outbox.SettlementObservation) {
				once.Do(func() {
					finishSubscriberSpan(span, obs)
				})
			}))
		return res, settlement
	}, nil
}

func finishSubscriberSpan(span Span, obs outbox.SettlementObservation) {
	span.SetAttributes(
		Attr{Key: "gocell.outbox.disposition", Value: obs.Disposition.String()},
		Attr{Key: "gocell.outbox.settlement.result", Value: string(obs.Result)},
	)

	switch obs.Result {
	case outbox.SettlementResultSuccess:
		finishSuccessfulSettlementSpan(span, obs)
	case outbox.SettlementResultRetryExhausted:
		span.SetStatus(StatusError, string(outbox.SettlementResultRetryExhausted))
		recordErr(span, obs.Err, "subscriber settlement retry exhausted")
	case outbox.SettlementResultCommitFailed:
		span.SetStatus(StatusError, string(outbox.SettlementResultCommitFailed))
		recordErr(span, obs.Err, "subscriber settlement commit failed")
	case outbox.SettlementResultAckFailed:
		span.SetStatus(StatusError, string(outbox.SettlementResultAckFailed))
		recordErr(span, obs.Err, "subscriber broker ack failed")
	case outbox.SettlementResultNackFailed:
		span.SetStatus(StatusError, string(outbox.SettlementResultNackFailed))
		recordErr(span, obs.Err, "subscriber broker nack failed")
	default:
		desc := string(obs.Result)
		if desc == "" {
			desc = "unknown settlement result"
		}
		span.SetStatus(StatusError, desc)
		recordErr(span, obs.Err, desc)
	}
	span.End()
}

func finishSuccessfulSettlementSpan(span Span, obs outbox.SettlementObservation) {
	switch obs.Disposition {
	case outbox.DispositionAck:
		span.SetStatus(StatusOK, "")
	case outbox.DispositionRequeue:
		span.SetStatus(StatusError, outbox.DispositionRequeue.String())
		recordErr(span, obs.Err, "subscriber settled Requeue without error")
	case outbox.DispositionReject:
		span.SetStatus(StatusError, outbox.DispositionReject.String())
		recordErr(span, obs.Err, "subscriber settled Reject without error")
	default:
		span.SetStatus(StatusError, "invalid disposition")
		recordErr(span, obs.Err,
			fmt.Sprintf("subscriber settled invalid disposition %d", obs.Disposition))
	}
}
