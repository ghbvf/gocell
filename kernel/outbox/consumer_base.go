package outbox

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
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

// leaseRenewalDivisor determines the default LeaseRenewalInterval as a fraction
// of LeaseTTL: interval = TTL / leaseRenewalDivisor. A value of 3 means renewal
// fires at 1/3 of the TTL, providing two retry attempts before the lease expires.
const leaseRenewalDivisor = 3

// exponentialDelayBase is the untyped-int scaling unit for ExponentialDelay:
// delay = base * (exponentialDelayBase << attempt). Must equal 1.
const exponentialDelayBase = 1

const (
	// defaultConsumerBaseRetryBaseDelay is the base delay for exponential-backoff
	// retry between handler invocations.
	defaultConsumerBaseRetryBaseDelay = 1 * time.Second
	// defaultConsumerBaseMaxRetryDelay caps the exponential-backoff delay to
	// prevent unbounded sleep intervals at high retry counts.
	defaultConsumerBaseMaxRetryDelay = 30 * time.Second
)

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
		c.RetryBaseDelay = defaultConsumerBaseRetryBaseDelay
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
		c.MaxRetryDelay = defaultConsumerBaseMaxRetryDelay
	}
	if c.LeaseRenewalInterval == 0 {
		c.LeaseRenewalInterval = c.LeaseTTL / leaseRenewalDivisor
	}
}

// cryptoRandInt64N returns a cryptographically random int64 in [0, n).
// Falls back to 0 on read error (safe degradation for jitter).
func cryptoRandInt64N(n int64) int64 {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 0
	}
	v := int64(binary.LittleEndian.Uint64(b[:]) & 0x7fffffffffffffff)
	return v % n
}

// ExponentialDelay computes base * 2^attempt with overflow protection,
// capped at maxDelay. Used by both claimWithRetry and retryLoop.
//
// This is the single source of truth for exponential-backoff delay
// computation; adapters (e.g., rabbitmq) should call this function
// instead of maintaining their own copies.
func ExponentialDelay(base, maxDelay time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 0 {
		return 0
	}
	maxSafeShift := 63 - bits.Len64(uint64(base))
	if attempt > maxSafeShift {
		return maxDelay
	}
	delay := base * (exponentialDelayBase << attempt)
	if delay <= 0 || delay > maxDelay {
		return maxDelay
	}
	return delay
}

// ConsumerBase wraps an EntryHandler with two-phase idempotency
// (Claim/Commit/Release) and exponential backoff retry. DLQ routing is
// handled by the broker via DLX (DispositionReject triggers Nack requeue=false).
//
// Settlement flows to the Subscriber delivery loop via the second return value
// of SubscriberHandler: ConsumerBase.Wrap returns (HandleResult, Settlement)
// so that Commit/Release can be called after broker Ack/Nack without leaking
// idempotency types into business code. Business handlers only return HandleResult.
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
	clk     clock.Clock

	// built marks the value as the product of NewConsumerBase rather than a
	// zero-value struct literal (`&ConsumerBase{}`). It is the single source of
	// truth consulted by IsConstructed; production wiring (runtime/bootstrap
	// phase6) refuses to start a subscription whose ConsumerBase did not pass
	// through the constructor, blocking the static-degradation footgun where a
	// literal-zero ConsumerBase would silently emit ClaimAcquired+nil receipt
	// on retryLoop attempt 0.
	built bool
}

// IsConstructed reports whether the ConsumerBase came from NewConsumerBase
// (true) rather than from a zero-value struct literal (false). Production
// wiring uses this to fail fast when a literal `&ConsumerBase{}` is fed into
// runtime/bootstrap.WithConsumerBase, preventing a silent retryLoop=0 path.
func (cb *ConsumerBase) IsConstructed() bool {
	return cb != nil && cb.built
}

// logWithContext delegates to slog.LogAttrs with the given context, ensuring
// any ContextHandler extracts observability fields (request_id, correlation_id,
// trace_id) restored by SubscriberWithMiddleware.SubscribeEntry on the consumer
// path (built-in outermost wrapper, no separate middleware to install).
func logWithContext(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	slog.LogAttrs(ctx, level, msg, attrs...)
}

// NewConsumerBase creates a ConsumerBase using the two-phase Claimer interface.
// Returns an error if ConsumerBaseConfig contains invalid values (e.g., unknown
// ClaimPolicy). The returned Receipt is threaded through HandleResult so that
// the Subscriber can Commit/Release after broker Ack/Nack.
//
// clk is the time source for backoff sleeps. It must be non-nil; pass
// clock.Real() in production and clockmock.New() in tests.
//
// ref: nats-go Connect() (*Conn, error), watermill-amqp NewSubscriber() (*Subscriber, error)
// — constructors return error, never panic.
func NewConsumerBase(claimer idempotency.Claimer, config ConsumerBaseConfig, clk clock.Clock) (*ConsumerBase, error) {
	clock.MustHaveClock(clk, "outbox.NewConsumerBase")
	if !config.ClaimPolicy.Valid() {
		return nil, fmt.Errorf("outbox: invalid ClaimPolicy %d (valid range: 0..%d)",
			config.ClaimPolicy, claimPolicySentinel-1)
	}
	config.SetDefaults()
	return &ConsumerBase{
		claimer: claimer,
		config:  config,
		clk:     clk,
		built:   true,
	}, nil
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
//   - DispositionReject (handler-explicit) -> Reject (broker routes to DLX)
//   - retry budget exhausted -> Reject
//   - ctx canceled / shutdown -> Requeue
//
// Wrap lifts a business EntryHandler into a SubscriberHandler that includes
// idempotency claim/release and retry logic. The returned SubscriberHandler
// is passed to Subscriber.Subscribe (not EntryHandler) so Settlement can
// be delivered to the Subscriber without leaking idempotency types into
// business code.
//
// Settlement is nil when ConsumerBase has no idempotency state: fail-open
// claim error, ClaimDone (already processed), or ClaimBusy (in progress).
// Subscribers MUST nil-check Settlement before calling Commit/Release.
func (cb *ConsumerBase) Wrap(sub Subscription, handler EntryHandler) SubscriberHandler {
	topic := sub.Topic
	consumerGroup := sub.ConsumerGroup
	return func(ctx context.Context, entry Entry) (HandleResult, Settlement) {
		idempotencyKey := fmt.Sprintf("%s:%s", consumerGroup, entry.ID)

		// Fail-open: single Claim attempt, proceed without idempotency on error.
		if cb.config.ClaimPolicy == ClaimPolicyFailOpen {
			state, receipt, err := cb.claimer.Claim(ctx, idempotencyKey, cb.config.LeaseTTL, cb.config.IdempotencyTTL)
			if err != nil {
				logWithContext(ctx, slog.LevelWarn, "outbox: idempotency claim failed, proceeding without receipt (fail-open)",
					slog.String(logKeyEventID, entry.ID),
					slog.String(logKeyTopic, topic),
					slog.String(logKeyConsumerGroup, consumerGroup),
					slog.Any("error", err))
				return cb.retryLoop(ctx, consumerGroup, topic, entry, handler), nil
			}
			return cb.handleClaimState(ctx, consumerGroup, topic, entry, handler, state, receipt)
		}

		// Fail-closed: claimWithRetry handles all attempts with backoff + jitter.
		state, receipt, err := cb.claimWithRetry(ctx, topic, entry, idempotencyKey, consumerGroup)
		if err != nil {
			logWithContext(ctx, slog.LevelError, "outbox: idempotency claim exhausted, requeuing (fail-closed)",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, consumerGroup),
				slog.Int("claim_retry_count", cb.config.ClaimRetryCount),
				slog.Any("error", err))
			return Requeue(err), nil
		}
		return cb.handleClaimState(ctx, consumerGroup, topic, entry, handler, state, receipt)
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
) (idempotency.ClaimState, idempotency.Receipt, error) {
	var lastErr error
	var zeroReceipt idempotency.Receipt

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
			// claim failed — no receipt acquired
			return 0, zeroReceipt, ctx.Err()
		}
		if attempt < cb.config.ClaimRetryCount-1 {
			base := ExponentialDelay(cb.config.ClaimRetryBaseDelay, cb.config.MaxRetryDelay, attempt)
			var jitter time.Duration
			if base > 0 {
				jitter = time.Duration(cryptoRandInt64N(int64(base/backoffJitterDivisor) + 1))
			}
			delay := min(base+jitter, cb.config.MaxRetryDelay)
			logWithContext(ctx, slog.LevelWarn, "outbox: idempotency claim failed, retrying locally",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, consumerGroup),
				slog.Int("attempt", attempt+1),
				slog.Int("max_retries", cb.config.ClaimRetryCount),
				slog.Duration("backoff", delay),
				slog.Any("error", err))
			t := cb.clk.NewTimerAt(cb.clk.Now().Add(delay))
			select {
			case <-t.C():
				t.Stop()
			case <-ctx.Done():
				t.Stop()
				// claim failed — no receipt acquired
				return 0, zeroReceipt, ctx.Err()
			}
		}
	}

	return 0, zeroReceipt, lastErr
}

// handleClaimState dispatches on the Claim result state. Both fail-open and
// fail-closed paths share the same ClaimDone / ClaimBusy / ClaimAcquired logic.
// Returns (HandleResult, Settlement) so Settlement flows to the Subscriber.
// Settlement is nil for ClaimDone and ClaimBusy (no idempotency state to settle).
func (cb *ConsumerBase) handleClaimState(
	ctx context.Context,
	consumerGroup string,
	topic string,
	entry Entry,
	handler EntryHandler,
	state idempotency.ClaimState,
	receipt idempotency.Receipt,
) (HandleResult, Settlement) {
	switch state {
	case idempotency.ClaimDone:
		logWithContext(ctx, slog.LevelDebug, "outbox: event already processed, skipping",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic))
		return Ack(), nil
	case idempotency.ClaimBusy:
		delay := cb.config.RetryBaseDelay
		logWithContext(ctx, slog.LevelDebug, "outbox: event being processed by another consumer, requeuing after backoff",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic),
			slog.Duration("backoff", delay))
		t := cb.clk.NewTimerAt(cb.clk.Now().Add(delay))
		select {
		case <-t.C():
			t.Stop()
		case <-ctx.Done():
			t.Stop()
		}
		return Requeue(nil), nil
	default:
		// ClaimAcquired -- start lease-renewal goroutine before invoking handler.
		result := cb.runWithRenewal(ctx, consumerGroup, topic, entry, handler, receipt)
		return result, receipt
	}
}

// requeueResult constructs a Requeue HandleResult with the given error and
// SettlementObservers. Observers from the last handler invocation are
// propagated so business-middleware observers are notified on ctx-cancel abort
// and other early-exit paths.
// Settlement is returned separately by the Wrap closure.
func requeueResult(err error, observers []SettlementObserver) HandleResult {
	return HandleResult{
		Disposition:         DispositionRequeue,
		Err:                 err,
		SettlementObservers: observers,
	}
}

// isPermanentRejection reports whether the handler returned an explicit
// permanent rejection. After 029 #03 ADR Decision 4, ConsumerBase no longer
// upgrades PermanentError-wrapped errors to Reject — handlers must be
// explicit (return DispositionReject) to route to DLX. PermanentError
// remains as a classification tag for logging/metrics, with no behavioral
// effect on Disposition.
func isPermanentRejection(result HandleResult) bool {
	return result.Disposition == DispositionReject
}

// waitBackoff sleeps for exponential backoff before the next retry, returning
// true if it should abort (ctx canceled) instead of retrying.
func (cb *ConsumerBase) waitBackoff(ctx context.Context, topic string, entry Entry, attempt int, lastErr error) (abort bool) {
	if ctx.Err() != nil {
		return true
	}
	delay := ExponentialDelay(cb.config.RetryBaseDelay, cb.config.MaxRetryDelay, attempt)
	logWithContext(ctx, slog.LevelWarn, "outbox: transient error, retrying",
		slog.String(logKeyEventID, entry.ID),
		slog.String(logKeyTopic, topic),
		slog.Int("attempt", attempt+1),
		slog.Int("max_retries", cb.config.RetryCount),
		slog.Duration("backoff", delay),
		slog.Any("error", lastErr))

	t := cb.clk.NewTimerAt(cb.clk.Now().Add(delay))
	select {
	case <-t.C():
		t.Stop()
		return false
	case <-ctx.Done():
		t.Stop()
		return true
	}
}

// retryLoop executes the handler with exponential backoff retries.
// Settlement is no longer threaded through HandleResult — it is returned
// by the Wrap closure alongside HandleResult via SubscriberHandler.
//
// SettlementObservers from the last handler invocation are propagated to the
// final returned HandleResult so that business-middleware observers (e.g.
// ConfigEventMiddleware) are notified after ConsumerBase resolves the final
// broker disposition.
func (cb *ConsumerBase) retryLoop(
	ctx context.Context,
	consumerGroup string,
	topic string,
	entry Entry,
	handler EntryHandler,
) HandleResult {
	var lastResult HandleResult
	for attempt := range cb.config.RetryCount {
		lastResult = handler(ctx, entry)
		if lastResult.Disposition == DispositionAck {
			return HandleResult{
				Disposition:         DispositionAck,
				ProcessReason:       lastResult.ProcessReason,
				SettlementObservers: lastResult.SettlementObservers,
			}
		}

		if isPermanentRejection(lastResult) {
			logWithContext(ctx, slog.LevelError, "outbox: handler rejected entry, routing to DLX",
				slog.String(logKeyEventID, entry.ID),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, consumerGroup),
				slog.Any("error", lastResult.Err))
			return HandleResult{
				Disposition:         DispositionReject,
				Err:                 lastResult.Err,
				ProcessReason:       lastResult.ProcessReason,
				SettlementObservers: lastResult.SettlementObservers,
			}
		}

		// Transient error — backoff before retry (skipped on the final attempt).
		if attempt < cb.config.RetryCount-1 {
			if cb.waitBackoff(ctx, topic, entry, attempt, lastResult.Err) {
				// Settlement.Release is called by the Subscriber after broker Nack.
				return requeueResult(ctx.Err(), lastResult.SettlementObservers)
			}
		}
	}

	// Context canceled during or after final attempt — requeue for redelivery
	// rather than routing to DLX. This ensures graceful shutdown does not
	// permanently discard in-flight messages.
	if ctx.Err() != nil {
		return requeueResult(ctx.Err(), lastResult.SettlementObservers)
	}

	// Exhausted all retries -- reject so broker routes to DLX.
	logWithContext(ctx, slog.LevelWarn, "outbox: retry budget exhausted, rejecting to DLX",
		slog.String(logKeyEventID, entry.ID),
		slog.String(logKeyTopic, topic),
		slog.String(logKeyConsumerGroup, consumerGroup),
		slog.Int("retry_count", cb.config.RetryCount),
		slog.String("process_reason", "retry_exhausted"),
		slog.Any("error", lastResult.Err))
	return HandleResult{
		Disposition:         DispositionReject,
		Err:                 lastResult.Err,
		ProcessReason:       "retry_exhausted",
		SettlementObservers: lastResult.SettlementObservers,
	}
}

// runWithRenewal starts a background lease-renewal goroutine and then invokes
// retryLoop with a cancellable context. If Extend returns ErrLeaseExpired the
// context is canceled so the handler can detect it via ctx.Done().
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
	consumerGroup string,
	topic string,
	entry Entry,
	handler EntryHandler,
	receipt idempotency.Receipt,
) HandleResult {
	interval := cb.config.LeaseRenewalInterval
	// Skip renewal when disabled (negative) or receipt is nil.
	if interval <= 0 || receipt == nil {
		return cb.retryLoop(ctx, consumerGroup, topic, entry, handler)
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

	result := cb.retryLoop(renewCtx, consumerGroup, topic, entry, handler)

	// Signal the renewal goroutine to stop and wait for it.
	cancelRenew()
	<-done

	// Hard fence: if the lease was lost during processing and the handler
	// still returned Ack (e.g., it ignored ctx.Done()), force-downgrade to
	// Requeue so a stale holder cannot commit a dead lease.
	// Settlement (receipt) is returned by handleClaimState alongside this result;
	// Subscriber will call Settlement.Release on Requeue disposition.
	if leaseLost.Load() && result.Disposition == DispositionAck {
		logWithContext(ctx, slog.LevelWarn, "outbox: lease lost during processing, downgrading Ack to Requeue (hard fence)",
			slog.String(logKeyEventID, entry.ID),
			slog.String(logKeyTopic, topic))
		return HandleResult{
			Disposition:         DispositionRequeue,
			Err:                 idempotency.ErrLeaseExpired,
			ProcessReason:       result.ProcessReason,
			SettlementObservers: result.SettlementObservers,
		}
	}

	return result
}

// leaseRenewalLoop ticks every interval and extends the processing lease.
// It calls onLeaseLost (which sets the leaseLost latch and cancels the handler
// context) if Extend returns ErrLeaseExpired (fencing failure).
// Exits when ctx is canceled (handler finished or lease lost).
func (cb *ConsumerBase) leaseRenewalLoop(
	ctx context.Context,
	topic string,
	entry Entry,
	receipt idempotency.Receipt,
	interval time.Duration,
	onLeaseLost func(),
) {
	ticker := cb.clk.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			extendCtx := context.WithoutCancel(ctx)
			if err := receipt.Extend(extendCtx, cb.config.LeaseTTL); err != nil {
				if errors.Is(err, idempotency.ErrLeaseExpired) {
					logWithContext(ctx, slog.LevelError, "outbox: lease lost during processing, canceling handler",
						slog.String(logKeyEventID, entry.ID),
						slog.String(logKeyTopic, topic))
					onLeaseLost()
					return
				}
				logWithContext(ctx, slog.LevelWarn, "outbox: lease extend failed (transient), will retry",
					slog.String(logKeyEventID, entry.ID),
					slog.String(logKeyTopic, topic),
					slog.Any("error", err))
			}
		}
	}
}
