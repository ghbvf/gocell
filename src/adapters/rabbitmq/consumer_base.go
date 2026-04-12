package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"math/rand/v2"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Structured log field keys used across consumer_base and subscriber.
const (
	logKeyEventID       = "event_id"
	logKeyTopic         = "topic"
	logKeyConsumerGroup = "consumer_group"
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

	// ClaimRetryCount is the max number of Claim() attempts on the fail-closed
	// path before returning DispositionRequeue to the broker.
	// Default: falls back to RetryCount (3).
	ClaimRetryCount int

	// ClaimRetryBaseDelay is the initial backoff delay between Claim() retries.
	// Default: falls back to RetryBaseDelay (1s).
	ClaimRetryBaseDelay time.Duration

	// MaxRetryDelay caps the exponential backoff delay for both claimWithRetry
	// and retryLoop, preventing unbounded growth with large retry counts.
	// Default: 30s.
	MaxRetryDelay time.Duration
}

func (c *ConsumerBaseConfig) setDefaults() {
	if c.RetryCount <= 0 {
		c.RetryCount = 3
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = 1 * time.Second
	}
	if c.IdempotencyTTL <= 0 {
		c.IdempotencyTTL = idempotency.DefaultTTL
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = idempotency.DefaultLeaseTTL
	}
	if c.ClaimRetryCount <= 0 {
		c.ClaimRetryCount = c.RetryCount
	}
	if c.ClaimRetryBaseDelay <= 0 {
		c.ClaimRetryBaseDelay = c.RetryBaseDelay
	}
	if c.MaxRetryDelay <= 0 {
		c.MaxRetryDelay = 30 * time.Second
	}
}

// safeDelay computes base * 2^attempt with overflow protection, capped by maxDelay.
// Uses bits.Len64 to determine the maximum safe shift, matching the strategy
// in Connection.backoffDelay.
func safeDelay(base, maxDelay time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	maxSafeShift := 63 - bits.Len64(uint64(base))
	if attempt > maxSafeShift {
		return maxDelay
	}
	delay := base * (1 << uint(attempt))
	if delay <= 0 || delay > maxDelay {
		return maxDelay
	}
	return delay
}

// ConsumerBase wraps an outbox.EntryHandler with two-phase idempotency
// (Claim/Commit/Release) and exponential backoff retry. DLQ routing is
// handled by the broker via DLX (DispositionReject triggers Nack requeue=false).
//
// The Receipt is threaded through HandleResult so processDelivery can
// Commit/Release after broker Ack/Nack succeeds.
//
// Consumer: cg-{ConsumerGroup}-{topic}
// Idempotency key: {ConsumerGroup}:{event-id}, TTL 24h
// ACK timing: after business logic returns DispositionAck
// Retry: transient errors -> retry+backoff / permanent errors -> DispositionReject → DLX
type ConsumerBase struct {
	claimer idempotency.Claimer
	config  ConsumerBaseConfig
}

func logWithContext(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	slog.LogAttrs(ctx, level, msg, attrs...)
}

// NewConsumerBase creates a ConsumerBase using the two-phase Claimer interface.
// The returned Receipt is threaded through HandleResult so that the Subscriber
// can Commit/Release after broker Ack/Nack.
func NewConsumerBase(claimer idempotency.Claimer, config ConsumerBaseConfig) *ConsumerBase {
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
// two-phase Claim/Commit/Release idempotency and retry with exponential backoff.
//
// The Receipt is threaded through HandleResult — ConsumerBase never calls
// Commit/Release itself; that is processDelivery's job after broker Ack/Nack.
//
// Fail-open (ClaimFailOpen=true): single Claim attempt; on error, proceed
// without idempotency — avoids total consumer stall, but risks duplicate
// processing during outage.
//
// Fail-closed (ClaimFailOpen=false or nil, default): all Claim attempts go
// through claimWithRetry (including the first), so every failure is followed
// by exponential backoff + jitter. Safe from duplicates, but all consumption
// stops until the idempotency backend recovers.
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
	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		idempotencyKey := fmt.Sprintf("%s:%s", cb.config.ConsumerGroup, entry.ID)

		// Fail-open: single Claim attempt, proceed without idempotency on error.
		if cb.config.ClaimFailOpen != nil && *cb.config.ClaimFailOpen {
			state, receipt, err := cb.claimer.Claim(ctx, idempotencyKey, cb.config.LeaseTTL, cb.config.IdempotencyTTL)
			if err != nil {
				logWithContext(ctx, slog.LevelWarn, "rabbitmq: idempotency claim failed, proceeding without receipt (fail-open)",
					slog.String(logKeyEventID, entry.ID),
					slog.String(logKeyTopic, topic),
					slog.String(logKeyConsumerGroup, cb.config.ConsumerGroup),
					slog.String("error", err.Error()))
				return cb.retryLoop(ctx, topic, entry, handler, nil)
			}
			return cb.handleClaimState(ctx, topic, entry, handler, state, receipt)
		}

		// Fail-closed: claimWithRetry handles all attempts with backoff + jitter.
		state, receipt, err := cb.claimWithRetry(ctx, topic, entry, idempotencyKey)
		if err != nil {
			logWithContext(ctx, slog.LevelError, "rabbitmq: idempotency claim exhausted, requeuing (fail-closed)",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, cb.config.ConsumerGroup),
				slog.Int("claim_retry_count", cb.config.ClaimRetryCount),
				slog.String("error", err.Error()))
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: err}
		}
		return cb.handleClaimState(ctx, topic, entry, handler, state, receipt)
	}
}

// claimWithRetry attempts Claimer.Claim up to ClaimRetryCount times with
// exponential backoff + jitter. It handles ALL attempts including the first —
// there is no separate naked Claim call before this function.
//
// This prevents the hot-loop that would occur if we immediately returned
// DispositionRequeue on a Claim failure — RabbitMQ's Nack(requeue=true)
// redelivers immediately, so without local retry the broker, CPU and logs
// would be hammered on every redelivery cycle.
//
// Backoff uses ClaimRetryBaseDelay with exponential growth, capped by
// MaxRetryDelay, plus random jitter in [0, base/2) to avoid thundering
// herd when multiple consumers retry against a recovering backend.
//
// Only called on the fail-closed path; fail-open uses a single Claim attempt.
func (cb *ConsumerBase) claimWithRetry(
	ctx context.Context,
	topic string,
	entry outbox.Entry,
	idempotencyKey string,
) (idempotency.ClaimState, outbox.Receipt, error) {
	var lastErr error

	for attempt := 0; attempt < cb.config.ClaimRetryCount; attempt++ {
		state, receipt, err := cb.claimer.Claim(
			ctx,
			idempotencyKey,
			cb.config.LeaseTTL,
			cb.config.IdempotencyTTL,
		)
		if err == nil {
			return state, receipt, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		if attempt < cb.config.ClaimRetryCount-1 {
			base := safeDelay(cb.config.ClaimRetryBaseDelay, cb.config.MaxRetryDelay, attempt)
			var jitter time.Duration
			if base > 0 {
				jitter = time.Duration(rand.Int64N(int64(base/2) + 1))
			}
			delay := min(base+jitter, cb.config.MaxRetryDelay)
			logWithContext(ctx, slog.LevelWarn, "rabbitmq: idempotency claim failed, retrying locally",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.Int("attempt", attempt+1),
				slog.Int("max_retries", cb.config.ClaimRetryCount),
				slog.Duration("backoff", delay),
				slog.String("error", err.Error()))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
		}
	}

	return 0, nil, lastErr
}

// handleClaimState dispatches on the Claim result state. Both fail-open and
// fail-closed paths share the same ClaimDone / ClaimBusy / ClaimAcquired logic.
func (cb *ConsumerBase) handleClaimState(
	ctx context.Context,
	topic string,
	entry outbox.Entry,
	handler outbox.EntryHandler,
	state idempotency.ClaimState,
	receipt outbox.Receipt,
) outbox.HandleResult {
	switch state {
	case idempotency.ClaimDone:
		logWithContext(ctx, slog.LevelDebug, "rabbitmq: event already processed, skipping",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic),
			slog.String(logKeyConsumerGroup, cb.config.ConsumerGroup))
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	case idempotency.ClaimBusy:
		delay := cb.config.RetryBaseDelay
		logWithContext(ctx, slog.LevelDebug, "rabbitmq: event being processed by another consumer, requeuing after backoff",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic),
			slog.String(logKeyConsumerGroup, cb.config.ConsumerGroup),
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

// requeueResult constructs a Requeue HandleResult with the given error and receipt.
func requeueResult(err error, receipt outbox.Receipt) outbox.HandleResult {
	return outbox.HandleResult{
		Disposition: outbox.DispositionRequeue,
		Err:         err,
		Receipt:     receipt,
	}
}

// retryLoop executes the handler with exponential backoff retries.
// Receipt is threaded through HandleResult for processDelivery to
// Commit/Release after broker Ack/Nack.
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
		var permErr *outbox.PermanentError
		if lastResult.Disposition == outbox.DispositionReject ||
			(lastResult.Err != nil && errors.As(lastResult.Err, &permErr)) {
			logWithContext(ctx, slog.LevelWarn, "rabbitmq: permanent error, rejecting to DLX",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, cb.config.ConsumerGroup),
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
				return requeueResult(ctx.Err(), receipt)
			}

			delay := safeDelay(cb.config.RetryBaseDelay, cb.config.MaxRetryDelay, attempt)
			logWithContext(ctx, slog.LevelWarn, "rabbitmq: transient error, retrying",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.Int("attempt", attempt+1),
				slog.Int("max_retries", cb.config.RetryCount),
				slog.Duration("backoff", delay),
				slog.Any("error", lastResult.Err))

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				// Receipt.Release is deferred to processDelivery after broker Ack/Nack.
				return requeueResult(ctx.Err(), receipt)
			}
		}
	}

	// Context cancelled during or after final attempt — requeue for redelivery
	// rather than routing to DLX. This ensures graceful shutdown does not
	// permanently discard in-flight messages.
	if ctx.Err() != nil {
		return requeueResult(ctx.Err(), receipt)
	}

	// Exhausted all retries — reject so broker routes to DLX.
	logWithContext(ctx, slog.LevelError, "rabbitmq: retry budget exhausted, rejecting to DLX",
		slog.String(logKeyEventID, entry.ID),
		slog.String(logKeyTopic, topic),
		slog.String(logKeyConsumerGroup, cb.config.ConsumerGroup),
		slog.Int("retry_count", cb.config.RetryCount),
		slog.Any("error", lastResult.Err))
	return outbox.HandleResult{
		Disposition: outbox.DispositionReject,
		Err:         lastResult.Err,
		Receipt:     receipt,
	}
}
