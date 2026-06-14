package outbox

// Ack returns a HandleResult declaring successful processing. ConsumerBase
// will Ack the broker delivery and Commit the idempotency Receipt.
//
// ref: cloudevents/sdk-go/v2/protocol Receipt + ResultACK -- factory pattern
// for handler outcome types.
func Ack() HandleResult {
	return HandleResult{Disposition: DispositionAck}
}

// Requeue returns a HandleResult declaring transient failure. ConsumerBase
// will return the message to the broker for backoff retry; once the retry
// budget is exhausted the consumer escalates to Reject so the broker can
// route to DLX. err is optional — pass nil if the caller has no diagnostic
// to attach.
func Requeue(err error) HandleResult {
	return HandleResult{Disposition: DispositionRequeue, Err: err}
}

// Reject returns a HandleResult declaring permanent failure. ConsumerBase
// will Nack(requeue=false) so the broker routes to DLX. Wrap err with
// outbox.NewPermanentError to tag it for logging/metrics; the disposition
// itself is what the broker observes — PermanentError is purely diagnostic.
//
// Passing a nil err is accepted but reduces DLX observability — the
// downstream operator has no diagnostic to attach. Production handlers
// SHOULD pass a non-nil err (typically wrapped with NewPermanentError).
func Reject(err error) HandleResult {
	return HandleResult{Disposition: DispositionReject, Err: err}
}
