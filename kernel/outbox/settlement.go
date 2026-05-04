package outbox

import "context"

// Settlement is the broker-side commit/release handle that ConsumerBase
// delivers alongside HandleResult via SubscriberHandler. Subscriber
// implementations (adapters/rabbitmq, runtime/eventbus) call Commit before
// broker Ack and Release after broker Nack to finalize idempotency state.
//
// Business handlers MUST NOT see Settlement — it is part of SubscriberHandler
// (framework Subscriber-side hand-off), not EntryHandler (business return
// signature). The compile-time separation supersedes the prior
// HANDLER-RECEIPT-WRITE-01 archtest gate, which guarded a HandleResult.Receipt
// field that no longer exists (see 029 #12 K#12 PR-V1-OUTBOX-RECEIPT-EXTRACT).
//
// kernel/idempotency.Receipt implicitly satisfies Settlement: its Commit and
// Release methods match this interface, and its Extend method is hidden in
// the Settlement view (renewal stays inside ConsumerBase).
//
// ref: nats-io/nats.go jetstream/message.go Msg interface — settle ops
//      (Ack/Nak/Term/InProgress) abstracted into a single interface
// ref: IBM/sarama consumer_group.go ConsumeClaim(session ConsumerGroupSession,
//      claim ConsumerGroupClaim) — settle handle as explicit method parameter
//      rather than a ctx side-channel
type Settlement interface {
	Commit(ctx context.Context) error
	Release(ctx context.Context) error
}
