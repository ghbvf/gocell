package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// ConsumerBaseConfig configures ConsumerBase behavior.
type ConsumerBaseConfig struct {
	// ConsumerGroup identifies this consumer group for idempotency keys.
	ConsumerGroup string

	// RetryCount is the maximum number of retries for transient errors.
	// Default: 3.
	RetryCount int

	// RetryBaseDelay is the initial delay for exponential backoff retries.
	// Default: 1s.
	RetryBaseDelay time.Duration

	// IdempotencyTTL is the TTL for idempotency keys (done-key TTL for Claimer).
	// Default: 24h (idempotency.DefaultTTL).
	IdempotencyTTL time.Duration

	// LeaseTTL is the processing-lease TTL for the Claimer backend.
	// If a consumer crashes mid-processing, the lease expires after this
	// duration, allowing another consumer to re-claim.
	// Default: 5m (idempotency.DefaultLeaseTTL). Only used with Claimer.
	LeaseTTL time.Duration

	// ClaimFailOpen controls behavior when Claimer.Claim() fails due to
	// infrastructure errors (e.g., Redis down).
	//   true:  proceed without idempotency — avoids total consumer
	//          stall, but risks duplicate processing during outage.
	//   false (default): return DispositionRequeue — safe from duplicates, but all
	//          consumption stops until the idempotency backend recovers.
	// Must be set explicitly; nil defaults to fail-closed for safety.
	ClaimFailOpen *bool
}

func (c *ConsumerBaseConfig) setDefaults() {
	if c.RetryCount <= 0 {
		c.RetryCount = 3
	}
	if c.RetryBaseDelay == 0 {
		c.RetryBaseDelay = 1 * time.Second
	}
	if c.IdempotencyTTL == 0 {
		c.IdempotencyTTL = idempotency.DefaultTTL
	}
	if c.LeaseTTL == 0 {
		c.LeaseTTL = idempotency.DefaultLeaseTTL
	}
}

// PermanentError wraps an error to indicate it should not be retried
// and should be routed to the dead-letter queue.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	return fmt.Sprintf("permanent: %s", e.Err.Error())
}

func (e *PermanentError) Unwrap() error {
	return e.Err
}

// NewPermanentError wraps an error as a PermanentError.
func NewPermanentError(err error) *PermanentError {
	return &PermanentError{Err: err}
}

// ConsumerBase wraps an outbox.EntryHandler with idempotency checking and
// exponential backoff retry. DLQ routing is now handled by the broker via
// DLX (DispositionReject triggers Nack requeue=false).
//
// ConsumerBase supports two idempotency backends:
//   - Legacy Checker (via NewConsumerBase): single-phase TryProcess. Has a race
//     condition where the key is marked done before broker Ack. Retained for
//     backward compatibility.
//   - Claimer (via NewConsumerBaseWithClaimer): two-phase Claim/Commit/Release.
//     Receipt is threaded through HandleResult so processDelivery can Commit
//     only after broker Ack succeeds.
//
// Consumer: cg-{ConsumerGroup}-{topic}
// Idempotency key: {ConsumerGroup}:{event-id}, TTL 24h
// ACK timing: after business logic returns DispositionAck
// Retry: transient errors -> retry+backoff / permanent errors -> DispositionReject → DLX
type ConsumerBase struct {
	checker idempotency.Checker  // legacy, nil when using Claimer
	claimer idempotency.Claimer  // Solution B, nil when using legacy Checker
	config  ConsumerBaseConfig
}

// NewConsumerBase creates a ConsumerBase using the legacy Checker interface.
//
// Deprecated: Use NewConsumerBaseWithClaimer for correct two-phase idempotency
// that aligns idempotency state with broker acknowledgement.
// Scheduled for removal after all cells migrate to Claimer (target: Phase 3).
func NewConsumerBase(checker idempotency.Checker, config ConsumerBaseConfig) *ConsumerBase {
	config.setDefaults()
	return &ConsumerBase{
		checker: checker,
		config:  config,
	}
}

// NewConsumerBaseWithClaimer creates a ConsumerBase using the two-phase
// Claimer interface. The returned Receipt is threaded through HandleResult
// so that the Subscriber can Commit/Release after broker Ack/Nack.
func NewConsumerBaseWithClaimer(claimer idempotency.Claimer, config ConsumerBaseConfig) *ConsumerBase {
	config.setDefaults()
	return &ConsumerBase{
		claimer: claimer,
		config:  config,
	}
}

// AsMiddleware returns a TopicHandlerMiddleware that applies this
// ConsumerBase's idempotency/retry wrapping to any EntryHandler.
// It can be used with SubscriberWithMiddleware to transparently inject
// ConsumerBase behavior into a raw Subscriber pipeline.
func (cb *ConsumerBase) AsMiddleware() outbox.TopicHandlerMiddleware {
	return func(topic string, next outbox.EntryHandler) outbox.EntryHandler {
		return cb.Wrap(topic, next)
	}
}

// Wrap returns an EntryHandler that wraps the given business handler with
// idempotency checking and retry with exponential backoff.
//
// When constructed with NewConsumerBaseWithClaimer, Wrap uses two-phase
// idempotency: the Receipt is attached to HandleResult so processDelivery
// can Commit/Release after broker Ack/Nack. When constructed with
// NewConsumerBase (legacy), Wrap uses the single-phase TryProcess path.
//
// Rules:
//   - handler returns DispositionAck → pass through as Ack
//   - handler returns DispositionRequeue → pass through as Requeue
//   - handler returns DispositionReject → pass through as Reject
//   - handler returns error with non-Ack disposition → retry with backoff
//   - PermanentError → Reject (broker routes to DLX)
//   - retry budget exhausted → Reject
//   - ctx cancelled / shutdown → Requeue
func (cb *ConsumerBase) Wrap(topic string, handler outbox.EntryHandler) outbox.EntryHandler {
	if cb.claimer != nil {
		return cb.wrapWithClaimer(topic, handler)
	}
	return cb.wrapWithChecker(topic, handler)
}

// wrapWithChecker is the legacy path using single-phase TryProcess.
// Deprecated: has a race condition where the key is marked done before broker Ack.
func (cb *ConsumerBase) wrapWithChecker(topic string, handler outbox.EntryHandler) outbox.EntryHandler {
	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		idempotencyKey := fmt.Sprintf("%s:%s", cb.config.ConsumerGroup, entry.ID)

		shouldProcess, err := cb.checker.TryProcess(ctx, idempotencyKey, cb.config.IdempotencyTTL)
		if err != nil {
			slog.Warn("rabbitmq: idempotency check failed, proceeding with handler",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup),
				slog.String("error", err.Error()))
			shouldProcess = true
		}
		if !shouldProcess {
			slog.Debug("rabbitmq: event already processed, skipping",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup))
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}

		// Wrap legacy Checker as a Receipt so processDelivery can Release
		// after broker Ack/Nack, not before. This prevents the race where
		// releaseChecker runs before Nack, opening a duplicate-processing window.
		receipt := &checkerReceipt{checker: cb.checker, key: idempotencyKey}
		return cb.retryLoop(ctx, topic, entry, handler, receipt)
	}
}

// wrapWithClaimer is the Solution B path using two-phase Claim/Commit/Release.
// The Receipt is threaded through HandleResult — ConsumerBase never calls
// Commit/Release itself; that is processDelivery's job after broker Ack/Nack.
func (cb *ConsumerBase) wrapWithClaimer(topic string, handler outbox.EntryHandler) outbox.EntryHandler {
	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		idempotencyKey := fmt.Sprintf("%s:%s", cb.config.ConsumerGroup, entry.ID)

		state, receipt, err := cb.claimer.Claim(ctx, idempotencyKey, cb.config.LeaseTTL, cb.config.IdempotencyTTL)
		if err != nil {
			if cb.config.ClaimFailOpen != nil && *cb.config.ClaimFailOpen {
				slog.Error("rabbitmq: idempotency claim failed, proceeding without receipt (fail-open)",
					slog.String("event_id", entry.ID),
					slog.String("topic", topic),
					slog.String("consumer_group", cb.config.ConsumerGroup),
					slog.String("error", err.Error()))
				return cb.retryLoop(ctx, topic, entry, handler, nil)
			}
			slog.Error("rabbitmq: idempotency claim failed, requeuing (fail-closed)",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup),
				slog.String("error", err.Error()))
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: err}
		}

		switch state {
		case idempotency.ClaimDone:
			slog.Debug("rabbitmq: event already processed, skipping",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup))
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		case idempotency.ClaimBusy:
			// Backoff before requeue to prevent busy loop: RabbitMQ's
			// Nack(requeue=true) redelivers immediately with no delay.
			delay := cb.config.RetryBaseDelay
			slog.Debug("rabbitmq: event being processed by another consumer, requeuing after backoff",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup),
				slog.Duration("backoff", delay))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
			}
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue}
		default:
			// ClaimAcquired — proceed with handler, thread Receipt through.
			return cb.retryLoop(ctx, topic, entry, handler, receipt)
		}
	}
}

// retryLoop executes the handler with exponential backoff retries.
// Receipt (from Claimer or checkerReceipt) is threaded through HandleResult
// for processDelivery to Commit/Release after broker Ack/Nack.
func (cb *ConsumerBase) retryLoop(
	ctx context.Context,
	topic string,
	entry outbox.Entry,
	handler outbox.EntryHandler,
	receipt outbox.Receipt,
) outbox.HandleResult {
	var lastResult outbox.HandleResult
	for attempt := range cb.config.RetryCount {
		lastResult = handler(ctx, entry)
		if lastResult.Disposition == outbox.DispositionAck {
			return outbox.HandleResult{
				Disposition: outbox.DispositionAck,
				Receipt:     receipt,
			}
		}

		// Check if this is a permanent error / explicit rejection.
		// Note: if handler returns DispositionRequeue with a PermanentError,
		// the PermanentError takes precedence and upgrades to Reject (no retry).
		// This allows WrapLegacyHandler (which always returns Requeue) to still
		// have PermanentError detected and routed to DLX by ConsumerBase.
		var permErr *PermanentError
		if lastResult.Disposition == outbox.DispositionReject ||
			(lastResult.Err != nil && errors.As(lastResult.Err, &permErr)) {
			slog.Warn("rabbitmq: permanent error, rejecting to DLX",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup),
				slog.Any("error", lastResult.Err))
			return outbox.HandleResult{
				Disposition: outbox.DispositionReject,
				Err:         lastResult.Err,
				Receipt:     receipt,
			}
		}

		// Transient error — backoff before retry.
		if attempt < cb.config.RetryCount-1 {
			if ctx.Err() != nil {
				// Receipt.Release is deferred to processDelivery after broker Ack/Nack.
				return outbox.HandleResult{
					Disposition: outbox.DispositionRequeue,
					Err:         ctx.Err(),
					Receipt:     receipt,
				}
			}

			delay := cb.config.RetryBaseDelay * (1 << attempt)
			slog.Warn("rabbitmq: transient error, retrying",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.Int("attempt", attempt+1),
				slog.Int("max_retries", cb.config.RetryCount),
				slog.Duration("backoff", delay),
				slog.Any("error", lastResult.Err))

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				// Receipt.Release is deferred to processDelivery after broker Ack/Nack.
				return outbox.HandleResult{
					Disposition: outbox.DispositionRequeue,
					Err:         ctx.Err(),
					Receipt:     receipt,
				}
			}
		}
	}

	// Exhausted all retries — reject so broker routes to DLX.
	slog.Error("rabbitmq: retry budget exhausted, rejecting to DLX",
		slog.String("event_id", entry.ID),
		slog.String("topic", topic),
		slog.String("consumer_group", cb.config.ConsumerGroup),
		slog.Int("retry_count", cb.config.RetryCount),
		slog.Any("error", lastResult.Err))
	return outbox.HandleResult{
		Disposition: outbox.DispositionReject,
		Err:         lastResult.Err,
		Receipt:     receipt,
	}
}

// checkerReceipt adapts the legacy Checker to the Receipt interface so that
// processDelivery can manage idempotency state uniformly after broker Ack/Nack.
//
//   - Commit: no-op — TryProcess already marked the key as done.
//   - Release: calls checker.Release to allow redelivery.
type checkerReceipt struct {
	checker idempotency.Checker
	key     string
}

// Compile-time interface check.
var _ outbox.Receipt = (*checkerReceipt)(nil)

func (r *checkerReceipt) Commit(_ context.Context) error {
	// TryProcess already marked the key; nothing more to do.
	return nil
}

func (r *checkerReceipt) Release(ctx context.Context) error {
	return r.checker.Release(ctx, r.key)
}
