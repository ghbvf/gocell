package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
)

// Structured log field keys used across ConsumerBase and transport subscribers.
const (
	logKeyEventID       = "event_id"
	logKeyTopic         = "topic"
	logKeyConsumerGroup = "consumer_group"
)

// backoffJitterDivisor controls jitter range for ConsumerBase retry backoff:
// jitter ∈ [0, base/backoffJitterDivisor). Single-sided jitter (0..+50%)
// keeps minimum delay ≥ base, avoiding thundering herd on a recovering backend.
const backoffJitterDivisor = 2

// ClaimPolicy controls ConsumerBase behavior when Claimer.Claim() fails.
// The zero value (ClaimPolicyFailClosed) is the safe default.
type ClaimPolicy uint8

const (
	// ClaimPolicyFailClosed (default zero-value): retry Claim with exponential
	// backoff. Safe from duplicates, but consumption stops until the idempotency
	// backend recovers.
	ClaimPolicyFailClosed ClaimPolicy = iota

	// ClaimPolicyFailOpen: single Claim attempt; on error, proceed without
	// idempotency receipt. Avoids total consumer stall, but risks duplicate
	// processing during outage.
	ClaimPolicyFailOpen

	// claimPolicySentinel must remain last — add new values above this line.
	claimPolicySentinel
)

// Valid returns true if the ClaimPolicy is a recognized enum value.
func (p ClaimPolicy) Valid() bool {
	return p < claimPolicySentinel
}

// String returns the lowercase kebab-case name of the ClaimPolicy.
// Unknown values render as "unknown(N)".
// The zero value (ClaimPolicyFailClosed) is the safe-by-default Go convention:
// any unset ClaimPolicy field automatically uses the stricter fail-closed path.
func (p ClaimPolicy) String() string {
	switch p {
	case ClaimPolicyFailClosed:
		return "fail-closed"
	case ClaimPolicyFailOpen:
		return "fail-open"
	default:
		return fmt.Sprintf("unknown(%d)", p)
	}
}

// ConsumerBaseConfig configures ConsumerBase behavior.
type ConsumerBaseConfig struct {
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

	// ClaimPolicy controls behavior when Claimer.Claim() fails due to
	// infrastructure errors (e.g., Redis down). See ClaimPolicyFailClosed
	// (default zero-value) and ClaimPolicyFailOpen for details.
	ClaimPolicy ClaimPolicy

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

	// LeaseRenewalInterval is how often the lease renewal goroutine calls
	// receipt.Extend while the handler is running. Zero falls back to
	// LeaseTTL/3 so the lease is renewed well before it expires.
	// Set to a negative value to disable lease renewal entirely.
	LeaseRenewalInterval time.Duration
}

// SetDefaults populates zero-valued fields with safe defaults. Called
// automatically by NewConsumerBase; exported so callers constructing the
// config outside of NewConsumerBase (e.g., test harnesses verifying default
// values) can invoke it directly.
func (c *ConsumerBaseConfig) SetDefaults() {
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
	if c.LeaseRenewalInterval == 0 {
		c.LeaseRenewalInterval = c.LeaseTTL / 3
	}
}

// exponentialDelay computes base * 2^attempt with overflow protection,
// capped at maxDelay. Used by both claimWithRetry and retryLoop.
func exponentialDelay(base, maxDelay time.Duration, attempt int) time.Duration {
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

// ConsumerBase wraps an EntryHandler with two-phase idempotency
// (Claim/Commit/Release) and exponential backoff retry. DLQ routing is
// handled by the broker via DLX (DispositionReject triggers Nack requeue=false).
//
// The Receipt is threaded through HandleResult so the subscriber's delivery
// loop can Commit/Release after broker Ack/Nack succeeds.
//
// Lives in kernel/outbox rather than adapters/rabbitmq because the logic is
// broker-agnostic — it only depends on kernel/idempotency + outbox types —
// and is wired by runtime/bootstrap alongside any transport that speaks the
// Subscriber interface. Adapters (rabbitmq, nats, kafka) reuse this middleware
// unchanged.
//
// Consumer: cg-{ConsumerGroup}-{topic}
// Idempotency key: {ConsumerGroup}:{event-id}, TTL 24h
// ACK timing: after business logic returns DispositionAck
// Retry: transient errors -> retry+backoff / permanent errors -> DispositionReject → DLX
//
// ref: ThreeDotsLabs/watermill message/router.go — router-level retry/poison/dedup
// ref: MassTransit UseMessageRetry — pipeline middleware at receive endpoint
// ref: NATS JetStream consumer_config AckWait+MaxDeliver+BackOff — subscriber config
type ConsumerBase struct {
	claimer idempotency.Claimer
	config  ConsumerBaseConfig
}

// logWithContext delegates to slog.LogAttrs with the given context, ensuring
// any ContextHandler extracts observability fields (request_id, correlation_id,
// trace_id) restored by ObservabilityContextMiddleware on the consumer path.
func logWithContext(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	slog.LogAttrs(ctx, level, msg, attrs...)
}

// NewConsumerBase creates a ConsumerBase using the two-phase Claimer interface.
// Returns an error if ConsumerBaseConfig contains invalid values (e.g., unknown
// ClaimPolicy). The returned Receipt is threaded through HandleResult so that
// the Subscriber can Commit/Release after broker Ack/Nack.
//
// ref: nats-go Connect() (*Conn, error), watermill-amqp NewSubscriber() (*Subscriber, error)
// — constructors return error, never panic.
func NewConsumerBase(claimer idempotency.Claimer, config ConsumerBaseConfig) (*ConsumerBase, error) {
	if !config.ClaimPolicy.Valid() {
		return nil, fmt.Errorf("outbox: invalid ClaimPolicy %d (valid range: 0..%d)",
			config.ClaimPolicy, claimPolicySentinel-1)
	}
	config.SetDefaults()
	return &ConsumerBase{
		claimer: claimer,
		config:  config,
	}, nil
}

// AsMiddleware returns a SubscriptionMiddleware that applies this
// ConsumerBase's idempotency/retry wrapping to any EntryHandler.
// It can be used with SubscriberWithMiddleware to transparently inject
// ConsumerBase behavior into a raw Subscriber pipeline.
func (cb *ConsumerBase) AsMiddleware() SubscriptionMiddleware {
	return func(sub Subscription, next EntryHandler) EntryHandler {
		return cb.Wrap(sub, next)
	}
}

// Wrap returns an EntryHandler that wraps the given business handler with
// two-phase Claim/Commit/Release idempotency and retry with exponential backoff.
//
// The idempotency key is constructed as "{sub.ConsumerGroup}:{entry.ID}",
// ensuring cross-cell fanout correctness: each cell's ConsumerGroup forms a
// separate namespace so ClaimDone in one cell does not silence another.
//
// The Receipt is threaded through HandleResult -- ConsumerBase never calls
// Commit/Release itself; that is the delivery loop's job after broker Ack/Nack.
//
// Fail-open (ClaimPolicyFailOpen): single Claim attempt; on error, proceed
// without idempotency -- avoids total consumer stall, but risks duplicate
// processing during outage.
//
// Fail-closed (ClaimPolicyFailClosed, default zero-value): all Claim attempts
// go through claimWithRetry (including the first), so every failure is followed
// by exponential backoff + jitter. Safe from duplicates, but all consumption
// stops until the idempotency backend recovers.
//
// Rules:
//   - handler returns DispositionAck -> pass through as Ack
//   - handler returns DispositionRequeue -> pass through as Requeue
//   - handler returns DispositionReject -> pass through as Reject
//   - handler returns error with non-Ack disposition -> retry with backoff
//   - PermanentError -> Reject (broker routes to DLX)
//   - retry budget exhausted -> Reject
//   - ctx cancelled / shutdown -> Requeue
func (cb *ConsumerBase) Wrap(sub Subscription, handler EntryHandler) EntryHandler {
	topic := sub.Topic
	consumerGroup := sub.ConsumerGroup
	return func(ctx context.Context, entry Entry) HandleResult {
		idempotencyKey := fmt.Sprintf("%s:%s", consumerGroup, entry.ID)

		// Fail-open: single Claim attempt, proceed without idempotency on error.
		if cb.config.ClaimPolicy == ClaimPolicyFailOpen {
			state, receipt, err := cb.claimer.Claim(ctx, idempotencyKey, cb.config.LeaseTTL, cb.config.IdempotencyTTL)
			if err != nil {
				logWithContext(ctx, slog.LevelWarn, "outbox: idempotency claim failed, proceeding without receipt (fail-open)",
					slog.String(logKeyEventID, entry.ID),
					slog.String(logKeyTopic, topic),
					slog.String(logKeyConsumerGroup, consumerGroup),
					slog.String("error", err.Error()))
				return cb.retryLoop(ctx, topic, entry, handler, nil)
			}
			return cb.handleClaimState(ctx, topic, entry, handler, state, receipt)
		}

		// Fail-closed: claimWithRetry handles all attempts with backoff + jitter.
		state, receipt, err := cb.claimWithRetry(ctx, topic, entry, idempotencyKey, consumerGroup)
		if err != nil {
			logWithContext(ctx, slog.LevelError, "outbox: idempotency claim exhausted, requeuing (fail-closed)",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, consumerGroup),
				slog.Int("claim_retry_count", cb.config.ClaimRetryCount),
				slog.String("error", err.Error()))
			return HandleResult{Disposition: DispositionRequeue, Err: err}
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
// Only called on the fail-closed path; fail-open uses a single Claim attempt.
func (cb *ConsumerBase) claimWithRetry(
	ctx context.Context,
	topic string,
	entry Entry,
	idempotencyKey string,
	consumerGroup string,
) (idempotency.ClaimState, Receipt, error) {
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
			base := exponentialDelay(cb.config.ClaimRetryBaseDelay, cb.config.MaxRetryDelay, attempt)
			var jitter time.Duration
			if base > 0 {
				jitter = time.Duration(rand.Int64N(int64(base/backoffJitterDivisor) + 1))
			}
			delay := min(base+jitter, cb.config.MaxRetryDelay)
			logWithContext(ctx, slog.LevelWarn, "outbox: idempotency claim failed, retrying locally",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, consumerGroup),
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
	entry Entry,
	handler EntryHandler,
	state idempotency.ClaimState,
	receipt Receipt,
) HandleResult {
	switch state {
	case idempotency.ClaimDone:
		logWithContext(ctx, slog.LevelDebug, "outbox: event already processed, skipping",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic))
		return HandleResult{Disposition: DispositionAck}
	case idempotency.ClaimBusy:
		delay := cb.config.RetryBaseDelay
		logWithContext(ctx, slog.LevelDebug, "outbox: event being processed by another consumer, requeuing after backoff",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic),
			slog.Duration("backoff", delay))
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
		return HandleResult{Disposition: DispositionRequeue}
	default:
		// ClaimAcquired -- start lease-renewal goroutine before invoking handler.
		return cb.runWithRenewal(ctx, topic, entry, handler, receipt)
	}
}

// requeueResult constructs a Requeue HandleResult with the given error and receipt.
func requeueResult(err error, receipt Receipt) HandleResult {
	return HandleResult{
		Disposition: DispositionRequeue,
		Err:         err,
		Receipt:     receipt,
	}
}

// isPermanentRejection reports whether the handler's last result is terminal —
// either an explicit DispositionReject or any error wrapping a PermanentError.
// The second case lets WrapLegacyHandler (which always returns Requeue) still
// surface PermanentError to DLX.
func isPermanentRejection(result HandleResult) bool {
	if result.Disposition == DispositionReject {
		return true
	}
	if result.Err == nil {
		return false
	}
	var permErr *PermanentError
	return errors.As(result.Err, &permErr)
}

// waitBackoff sleeps for exponential backoff before the next retry, returning
// true if it should abort (ctx cancelled) instead of retrying.
func (cb *ConsumerBase) waitBackoff(ctx context.Context, topic string, entry Entry, attempt int, lastErr error) (abort bool) {
	if ctx.Err() != nil {
		return true
	}
	delay := exponentialDelay(cb.config.RetryBaseDelay, cb.config.MaxRetryDelay, attempt)
	logWithContext(ctx, slog.LevelWarn, "outbox: transient error, retrying",
		slog.String(logKeyEventID, entry.ID),
		slog.String(logKeyTopic, topic),
		slog.Int("attempt", attempt+1),
		slog.Int("max_retries", cb.config.RetryCount),
		slog.Duration("backoff", delay),
		slog.Any("error", lastErr))

	select {
	case <-time.After(delay):
		return false
	case <-ctx.Done():
		return true
	}
}

// retryLoop executes the handler with exponential backoff retries.
// Receipt is threaded through HandleResult for the subscriber's delivery loop
// to Commit/Release after broker Ack/Nack.
func (cb *ConsumerBase) retryLoop(
	ctx context.Context,
	topic string,
	entry Entry,
	handler EntryHandler,
	receipt Receipt,
) HandleResult {
	var lastResult HandleResult
	for attempt := range cb.config.RetryCount {
		lastResult = handler(ctx, entry)
		if lastResult.Disposition == DispositionAck {
			return HandleResult{
				Disposition: DispositionAck,
				Receipt:     receipt,
			}
		}

		if isPermanentRejection(lastResult) {
			logWithContext(ctx, slog.LevelWarn, "outbox: permanent error, rejecting to DLX",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.Any("error", lastResult.Err))
			return HandleResult{
				Disposition: DispositionReject,
				Err:         lastResult.Err,
				Receipt:     receipt,
			}
		}

		// Transient error — backoff before retry (skipped on the final attempt).
		if attempt < cb.config.RetryCount-1 {
			if cb.waitBackoff(ctx, topic, entry, attempt, lastResult.Err) {
				// Receipt.Release is deferred to the delivery loop after broker Ack/Nack.
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

	// Exhausted all retries -- reject so broker routes to DLX.
	logWithContext(ctx, slog.LevelError, "outbox: retry budget exhausted, rejecting to DLX",
		slog.String(logKeyEventID, entry.ID),
		slog.String(logKeyTopic, topic),
		slog.Int("retry_count", cb.config.RetryCount),
		slog.Any("error", lastResult.Err))
	return HandleResult{
		Disposition: DispositionReject,
		Err:         lastResult.Err,
		Receipt:     receipt,
	}
}

// runWithRenewal starts a background lease-renewal goroutine and then invokes
// retryLoop with a cancellable context. If Extend returns ErrLeaseExpired the
// context is cancelled so the handler can detect it via ctx.Done().
//
// Hard fence (Layer 1): an atomic.Bool latch tracks whether the lease was lost
// during processing. After retryLoop returns, if leaseLost is set AND the
// handler returned DispositionAck, the result is force-downgraded to
// DispositionRequeue. This prevents a stale handler that ignores ctx.Done()
// from successfully committing after losing its lease.
//
// The renewal goroutine exits after retryLoop returns (via cancel + done channel).
// context.WithoutCancel wraps the Extend ctx so a shutdown-triggered cancellation
// of the outer ctx does not prevent the last renewal/log from completing.
//
// Cognitive complexity is kept ≤15 by delegating the ticker loop to leaseRenewalLoop.
func (cb *ConsumerBase) runWithRenewal(
	ctx context.Context,
	topic string,
	entry Entry,
	handler EntryHandler,
	receipt Receipt,
) HandleResult {
	interval := cb.config.LeaseRenewalInterval
	// Skip renewal when disabled (negative) or receipt is nil.
	if interval <= 0 || receipt == nil {
		return cb.retryLoop(ctx, topic, entry, handler, receipt)
	}

	var leaseLost atomic.Bool

	renewCtx, cancelRenew := context.WithCancel(ctx)
	defer cancelRenew()

	done := make(chan struct{})
	go func() {
		defer close(done)
		cb.leaseRenewalLoop(renewCtx, topic, entry, receipt, interval, func() {
			leaseLost.Store(true)
			cancelRenew()
		})
	}()

	result := cb.retryLoop(renewCtx, topic, entry, handler, receipt)

	// Signal the renewal goroutine to stop and wait for it.
	cancelRenew()
	<-done

	// Hard fence: if the lease was lost during processing and the handler
	// still returned Ack (e.g., it ignored ctx.Done()), force-downgrade to
	// Requeue so a stale holder cannot commit a dead lease.
	if leaseLost.Load() && result.Disposition == DispositionAck {
		logWithContext(ctx, slog.LevelWarn, "outbox: lease lost during processing, downgrading Ack to Requeue (hard fence)",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic))
		return HandleResult{Disposition: DispositionRequeue, Receipt: receipt, Err: idempotency.ErrLeaseExpired}
	}

	return result
}

// leaseRenewalLoop ticks every interval and extends the processing lease.
// It calls onLeaseLost (which sets the leaseLost latch and cancels the handler
// context) if Extend returns ErrLeaseExpired (fencing failure).
// Exits when ctx is cancelled (handler finished or lease lost).
func (cb *ConsumerBase) leaseRenewalLoop(
	ctx context.Context,
	topic string,
	entry Entry,
	receipt Receipt,
	interval time.Duration,
	onLeaseLost func(),
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			extendCtx := context.WithoutCancel(ctx)
			if err := receipt.Extend(extendCtx, cb.config.LeaseTTL); err != nil {
				if errors.Is(err, idempotency.ErrLeaseExpired) {
					logWithContext(ctx, slog.LevelError, "outbox: lease lost during processing, cancelling handler",
						slog.String(logKeyEventID, entry.ID),
						slog.String(logKeyTopic, topic))
					onLeaseLost()
					return
				}
				logWithContext(ctx, slog.LevelWarn, "outbox: lease extend failed (transient), will retry",
					slog.String(logKeyEventID, entry.ID),
					slog.String(logKeyTopic, topic),
					slog.String("error", err.Error()))
			}
		}
	}
}
