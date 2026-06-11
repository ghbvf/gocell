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
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/observability"
)

// logKeyEventID is the slog structured-field key (snake_case per observability.md).
// The wire JSON header field declared in event headers.schema.json is "eventId" (camelCase).

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

	// Logger is the structured logger for all ConsumerBase log output (claim,
	// retry, DLX-reject and observer-panic lines). Nil defaults to slog.Default()
	// in SetDefaults, so production callers need not set it. Tests inject a
	// buffer-backed logger here to capture output without mutating the global
	// slog default (slog.SetDefault races with t.Parallel() siblings).
	//
	// ConsumerBase takes the logger as a config field (not a WithLogger option
	// like DirectEmitter) because SetDefaults resolves the nil fallback once at
	// construction.
	Logger *slog.Logger
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
	if c.Logger == nil {
		c.Logger = slog.Default()
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
// of SubscriberHandler: ConsumerBase.Wrap returns (DeliveryOutcome, Settlement)
// so that Commit/Release can be called after broker Ack/Nack without leaking
// idempotency types into business code. Business handlers only return the slim
// HandleResult; ConsumerBase lifts it to DeliveryOutcome, injecting
// ProcessReason (e.g. "retry_exhausted") as needed.
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

	// logger is the resolved structured logger (config.Logger after SetDefaults,
	// never nil). Every ConsumerBase log line and the observer-panic SafeObserve
	// logger derive from it, so a test-injected logger captures all output.
	logger *slog.Logger

	// observer receives notifications on terminal Reject paths. Initialized to
	// NopConsumerObserver{} by NewConsumerBase so it is never nil. Replaced at
	// most once via AttachObserver; observerAttached tracks whether a non-Nop
	// observer has been wired.
	observer         ConsumerObserver
	observerAttached bool

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

// logWithContext delegates to the injected logger's LogAttrs with the given
// context, ensuring any ContextHandler extracts observability fields
// (request_id, correlation_id, trace_id) restored by
// SubscriberWithMiddleware.SubscribeEntry on the consumer path (built-in
// outermost wrapper, no separate middleware to install).
func (cb *ConsumerBase) logWithContext(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	cb.logger.LogAttrs(ctx, level, msg, attrs...)
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
		claimer:  claimer,
		config:   config,
		clk:      clk,
		logger:   config.Logger, // non-nil: SetDefaults fell back to slog.Default()
		observer: NopConsumerObserver{},
		built:    true,
	}, nil
}

// AttachObserver wires a ConsumerObserver that receives notifications on every
// terminal Reject disposition (handler-explicit reject or retry-budget
// exhaustion). AttachObserver may be called at most once per ConsumerBase;
// repeat calls return ErrObserverAlreadyAttached (wiring fail-fast per
// runtime-api.md §Option 范式分层). Bare-nil and typed-nil observers are
// rejected with ErrValidationFailed.
//
// AttachObserver is the single public injection path for ConsumerObserver. The
// internal observer field is private and is not exposed via ConsumerBaseConfig —
// ConsumerBase is typically constructed at composition root (cmd/corebundle)
// before the metrics provider is wired in bootstrap phase 5, so constructor
// injection is not viable. bootstrap.autoWireOutboxRejectCollector calls
// AttachObserver in phase 6 (before subscriptions start consuming); business
// code typically does not call this directly.
//
// ref: Temporal MetricsHandler observer-injected pattern
func (cb *ConsumerBase) AttachObserver(o ConsumerObserver) error {
	if isNilObserver(o) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: AttachObserver requires a non-nil ConsumerObserver")
	}
	if cb.observerAttached {
		return ErrObserverAlreadyAttached
	}
	cb.observer = o
	cb.observerAttached = true
	return nil
}

// deliveryFrom converts a slim HandleResult into a DeliveryOutcome. It copies
// only the fields that business handlers can express (Disposition + Err); the
// subscriber-layer fields (ProcessReason, SettlementObservers) start empty and
// are filled in by ConsumerBase / subscriber-layer middleware.
func deliveryFrom(r HandleResult) DeliveryOutcome {
	return DeliveryOutcome{Disposition: r.Disposition, Err: r.Err}
}

// Wrap returns a SubscriberHandler that wraps the given business handler with
// two-phase Claim/Commit/Release idempotency and retry with exponential backoff.
//
// The idempotency key is constructed as "{sub.ConsumerGroup}:{entry.id}",
// ensuring cross-cell fanout correctness: each cell's ConsumerGroup forms a
// separate namespace so ClaimDone in one cell does not silence another.
//
// The Receipt is threaded through DeliveryOutcome -- ConsumerBase never calls
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
//   - retry budget exhausted -> Reject with ProcessReason="retry_exhausted"
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
	cellID := sub.CellID
	passthrough := cb.brokerDelayPassthrough(sub)
	dims := deliveryDims{cellID: cellID, consumerGroup: consumerGroup, topic: topic}
	return func(ctx context.Context, entry Entry) (DeliveryOutcome, Settlement) {
		idempotencyKey := fmt.Sprintf("%s:%s", consumerGroup, entry.id)

		// Fail-open: single Claim attempt, proceed without idempotency on error.
		if cb.config.ClaimPolicy == ClaimPolicyFailOpen {
			state, receipt, err := cb.claimer.Claim(ctx, idempotencyKey, cb.config.LeaseTTL, cb.config.IdempotencyTTL)
			if err != nil {
				cb.logWithContext(ctx, slog.LevelWarn, "outbox: idempotency claim failed, proceeding without receipt (fail-open)",
					slog.String(logKeyEventID, entry.id),
					slog.String(logKeyTopic, topic),
					slog.String(logKeyConsumerGroup, consumerGroup),
					slog.Any("error", err))
				return cb.retryLoop(ctx, cellID, consumerGroup, topic, entry, handler, passthrough), nil
			}
			return cb.handleClaimState(ctx, dims, entry, handler, state, receipt, passthrough)
		}

		// Fail-closed: claimWithRetry handles all attempts with backoff + jitter.
		state, receipt, err := cb.claimWithRetry(ctx, topic, entry, idempotencyKey, consumerGroup)
		if err != nil {
			cb.logWithContext(ctx, slog.LevelError, "outbox: idempotency claim exhausted, requeuing (fail-closed)",
				slog.String(logKeyEventID, entry.id),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, consumerGroup),
				slog.Int("claim_retry_count", cb.config.ClaimRetryCount),
				slog.Any("error", err))
			return deliveryFrom(Requeue(err)), nil
		}
		return cb.handleClaimState(ctx, dims, entry, handler, state, receipt, passthrough)
	}
}

// brokerDelayPassthrough reports whether the subscription delegates retry timing
// to a broker/in-memory delay schedule (#1458). When true, ConsumerBase runs the
// handler exactly once and returns its verdict verbatim — it must NOT run its own
// in-process retry loop, and crucially must NOT convert a transient Requeue into
// the retry-exhausted Reject, because the transport (rabbitmq TTL+DLX delay
// tiers / in-memory schedule) owns the per-attempt delay, the retry budget, and
// the final DLX routing. Stacking ConsumerBase's exhaustion logic here would
// dead-letter the entry on the first failure and bypass the delay schedule.
func (cb *ConsumerBase) brokerDelayPassthrough(sub Subscription) bool {
	return len(sub.BrokerDelaySchedule) > 0
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
			cb.logWithContext(ctx, slog.LevelWarn, "outbox: idempotency claim failed, retrying locally",
				slog.String(logKeyEventID, entry.id),
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

// deliveryDims groups the observability and idempotency dimensions that flow
// together through handleClaimState → runWithRenewal → retryLoop. Bundling
// the three string fields into one struct keeps handleClaimState within the
// 7-parameter limit (go:S107) without altering the inner-function signatures.
type deliveryDims struct {
	cellID        string
	consumerGroup string
	topic         string
}

// handleClaimState dispatches on the Claim result state. Both fail-open and
// fail-closed paths share the same ClaimDone / ClaimBusy / ClaimAcquired logic.
// Returns (DeliveryOutcome, Settlement) so Settlement flows to the Subscriber.
// Settlement is nil for ClaimDone and ClaimBusy (no idempotency state to settle).
func (cb *ConsumerBase) handleClaimState(
	ctx context.Context,
	dims deliveryDims,
	entry Entry,
	handler EntryHandler,
	state idempotency.ClaimState,
	receipt idempotency.Receipt,
	passthrough bool,
) (DeliveryOutcome, Settlement) {
	topic := dims.topic
	switch state {
	case idempotency.ClaimDone:
		cb.logWithContext(ctx, slog.LevelDebug, "outbox: event already processed, skipping",
			slog.String(logKeyEventID, entry.id),
			slog.String(logKeyTopic, topic))
		return deliveryFrom(Ack()), nil
	case idempotency.ClaimBusy:
		delay := cb.config.RetryBaseDelay
		cb.logWithContext(ctx, slog.LevelDebug, "outbox: event being processed by another consumer, requeuing after backoff",
			slog.String(logKeyEventID, entry.id),
			slog.String(logKeyTopic, topic),
			slog.Duration("backoff", delay))
		t := cb.clk.NewTimerAt(cb.clk.Now().Add(delay))
		select {
		case <-t.C():
			t.Stop()
		case <-ctx.Done():
			t.Stop()
		}
		return deliveryFrom(Requeue(nil)), nil
	default:
		// ClaimAcquired -- start lease-renewal goroutine before invoking handler.
		result := cb.runWithRenewal(ctx, dims, entry, handler, receipt, passthrough)
		return result, receipt
	}
}

// requeueResult constructs a Requeue DeliveryOutcome with the given error.
// Settlement is returned separately by the Wrap closure.
func requeueResult(err error) DeliveryOutcome {
	return DeliveryOutcome{
		Disposition: DispositionRequeue,
		Err:         err,
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
	cb.logWithContext(ctx, slog.LevelWarn, "outbox: transient error, retrying",
		slog.String(logKeyEventID, entry.id),
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
// Settlement is returned by the Wrap closure alongside DeliveryOutcome via
// SubscriberHandler.
//
// cellID is the observability owner dimension forwarded to the ConsumerObserver
// on terminal Reject paths. It is captured from sub.CellID by Wrap and passed
// through runWithRenewal → retryLoop so no ambient state is required.
func (cb *ConsumerBase) retryLoop(
	ctx context.Context,
	cellID string,
	consumerGroup string,
	topic string,
	entry Entry,
	handler EntryHandler,
	passthrough bool,
) DeliveryOutcome {
	if passthrough {
		// Broker-delay subscriptions (#1458): the transport owns retry timing,
		// the retry budget, and DLX routing, so run the handler exactly once and
		// return its verdict verbatim. Crucially this skips the retry-exhausted
		// → Reject conversion below: a transient Requeue must reach the subscriber
		// as a Requeue so it can apply the per-attempt delay, not be dead-lettered.
		return deliveryFrom(handler(ctx, entry))
	}
	var lastResult HandleResult
	for attempt := range cb.config.RetryCount {
		lastResult = handler(ctx, entry)
		if lastResult.Disposition == DispositionAck {
			return deliveryFrom(lastResult)
		}

		if isPermanentRejection(lastResult) {
			cb.logWithContext(ctx, slog.LevelError, "outbox: handler rejected entry, routing to DLX",
				slog.String(logKeyEventID, entry.id),
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumerGroup, consumerGroup),
				slog.Any("error", lastResult.Err))
			observability.SafeObserve(cb.logger.With(
				slog.String("cell", cellID),
				slog.String("topic", topic),
				slog.String("consumer_group", consumerGroup),
			), func() {
				cb.observer.ObserveReject(ctx, cellID, topic, consumerGroup, ConsumerRejectReasonHandlerReject)
			})
			return deliveryFrom(lastResult)
		}

		// Transient error — backoff before retry (skipped on the final attempt).
		if attempt < cb.config.RetryCount-1 {
			if cb.waitBackoff(ctx, topic, entry, attempt, lastResult.Err) {
				// Settlement.Release is called by the Subscriber after broker Nack.
				return requeueResult(ctx.Err())
			}
		}
	}

	// Context canceled during or after final attempt — requeue for redelivery
	// rather than routing to DLX. This ensures graceful shutdown does not
	// permanently discard in-flight messages.
	if ctx.Err() != nil {
		return requeueResult(ctx.Err())
	}

	// Exhausted all retries -- reject so broker routes to DLX.
	// Upgraded from LevelWarn to LevelError: retry-exhausted routes to DLX
	// (correctness-affecting) per observability.md §slog 日志级别.
	cb.logWithContext(ctx, slog.LevelError, "outbox: retry budget exhausted, rejecting to DLX",
		slog.String(logKeyEventID, entry.id),
		slog.String(logKeyTopic, topic),
		slog.String(logKeyConsumerGroup, consumerGroup),
		slog.Int("retry_count", cb.config.RetryCount),
		slog.String("process_reason", ProcessReasonRetryExhausted),
		slog.Any("error", lastResult.Err))
	observability.SafeObserve(cb.logger.With(
		slog.String("cell", cellID),
		slog.String("topic", topic),
		slog.String("consumer_group", consumerGroup),
	), func() {
		cb.observer.ObserveReject(ctx, cellID, topic, consumerGroup, ConsumerRejectReasonRetryExhausted)
	})
	return DeliveryOutcome{
		Disposition:   DispositionReject,
		Err:           lastResult.Err,
		ProcessReason: ProcessReasonRetryExhausted,
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
	dims deliveryDims,
	entry Entry,
	handler EntryHandler,
	receipt idempotency.Receipt,
	passthrough bool,
) DeliveryOutcome {
	interval := cb.config.LeaseRenewalInterval
	// Skip renewal when disabled (negative) or receipt is nil.
	if interval <= 0 || receipt == nil {
		return cb.retryLoop(ctx, dims.cellID, dims.consumerGroup, dims.topic, entry, handler, passthrough)
	}

	var leaseLost atomic.Bool

	renewCtx, cancelRenew := context.WithCancel(ctx)
	defer cancelRenew()

	done := make(chan struct{})
	go func() {
		defer close(done)
		cb.leaseRenewalLoop(renewCtx, dims.topic, entry, receipt, interval, func() {
			leaseLost.Store(true)
			cancelRenew()
		})
	}()

	result := cb.retryLoop(renewCtx, dims.cellID, dims.consumerGroup, dims.topic, entry, handler, passthrough)

	// Signal the renewal goroutine to stop and wait for it.
	cancelRenew()
	<-done

	// Hard fence: if the lease was lost during processing and the handler
	// still returned Ack (e.g., it ignored ctx.Done()), force-downgrade to
	// Requeue so a stale holder cannot commit a dead lease.
	// Settlement (receipt) is returned by handleClaimState alongside this result;
	// Subscriber will call Settlement.Release on Requeue disposition.
	if leaseLost.Load() && result.Disposition == DispositionAck {
		cb.logWithContext(ctx, slog.LevelWarn, "outbox: lease lost during processing, downgrading Ack to Requeue (hard fence)",
			slog.String(logKeyEventID, entry.id),
			slog.String(logKeyTopic, dims.topic))
		return DeliveryOutcome{
			Disposition:   DispositionRequeue,
			Err:           idempotency.ErrLeaseExpired,
			ProcessReason: result.ProcessReason,
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
					cb.logWithContext(ctx, slog.LevelError, "outbox: lease lost during processing, canceling handler",
						slog.String(logKeyEventID, entry.id),
						slog.String(logKeyTopic, topic))
					onLeaseLost()
					return
				}
				cb.logWithContext(ctx, slog.LevelWarn, "outbox: lease extend failed (transient), will retry",
					slog.String(logKeyEventID, entry.id),
					slog.String(logKeyTopic, topic),
					slog.Any("error", err))
			}
		}
	}
}
