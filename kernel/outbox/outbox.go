// Package outbox defines interfaces for the transactional outbox pattern.
// Implementations live in adapters/ (e.g., adapters/postgres).
//
// ref: ThreeDotsLabs/watermill message/ -- Message unified model, Publisher/Subscriber interfaces
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/metautil"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// Metadata size limits (META-SIZE-01) live in kernel/metautil so kernel/outbox
// and kernel/command share a single source of truth. The values themselves
// (key count, key length, value length, total size) are imported via
// metautil.Max* constants and METADATA-LIMITS-SINGLE-SOURCE-01 archtest
// rejects any reintroduction in this package.

const (
	// MaxPayloadBytes caps Entry.Payload length. A bug or malicious producer
	// that emits multi-MB JSON would otherwise inflate relay batch memory
	// (BatchSize × payload bytes) and PG TOAST / replication overhead. 1 MiB
	// matches Apache Kafka's default `message.max.bytes` and gives audit
	// snapshots / large business events comfortable headroom while keeping a
	// single relay batch under tens of MB at the default BatchSize=100.
	// ref: Apache Kafka message.max.bytes default 1 MiB.
	MaxPayloadBytes = 1 << 20

	internalMetadataKeyQuotedFmt = "key=%q"
)

// ReservedMetadataKeys lists keys that the kernel observability bridge owns
// exclusively. Producers writing these into Entry.Metadata is a programming
// error: the typed Entry.Observability field is the canonical home for
// trace/request/correlation identity, and Entry.Validate rejects entries
// that try to forge them via the producer-owned Metadata namespace.
//
// The list is exhaustive — these are the only keys the kernel bridge maps
// in either direction. Adding a new bridge field requires extending this
// list (caught by reservedMetadataKeyMembership invariant test).
var ReservedMetadataKeys = []string{
	"trace_id",
	"traceparent",
	"trace_state",
	"tracestate",
	"span_id",
	"request_id",
	"correlation_id",
}

// reservedMetadataKeySet is the membership-test view of ReservedMetadataKeys.
// Built once at package init; lookup is O(1).
var reservedMetadataKeySet = func() map[string]struct{} {
	s := make(map[string]struct{}, len(ReservedMetadataKeys))
	for _, k := range ReservedMetadataKeys {
		s[k] = struct{}{}
	}
	return s
}()

// validateMetadata layers the outbox-specific reserved-key check on top of
// the shared metautil limits. nil or empty metadata is valid.
func validateMetadata(m map[string]string) error {
	for k := range m {
		if _, reserved := reservedMetadataKeySet[k]; reserved {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"outbox: metadata key is reserved for the observability bridge — use Entry.Observability instead",
				errcode.WithInternal(fmt.Sprintf(internalMetadataKeyQuotedFmt, k)))
		}
	}
	return metautil.ValidateLimits(m, metautil.DomainOutbox)
}

// ---------------------------------------------------------------------------
// Entry
// ---------------------------------------------------------------------------

// Entry represents a single outbox record to be published.
type Entry struct {
	// ID is the canonical idempotency identifier. Consumers SHOULD use this
	// field to construct idempotency keys.
	ID            string
	AggregateID   string
	AggregateType string
	EventType     string
	Topic         string // broker routing key; falls back to EventType if empty
	Payload       []byte
	CreatedAt     time.Time
	// Metadata carries optional business metadata (ref: Watermill Message.Metadata).
	// Producers may freely read and write this map for domain-specific key-value
	// pairs. Observability IDs (trace_id, request_id, etc.) must NOT be written
	// here — use the typed Observability field instead.
	Metadata map[string]string

	// Observability carries cross-async tracing context managed exclusively by
	// the gocell observability bridge. Producers MUST NOT populate this field
	// directly — (e *Entry).InjectObservabilityFromContext fills it from the
	// originating request context at write time. SubscriberWithMiddleware
	// (the canonical consumer wrapper) restores it into the handler context
	// before any user middleware as an OUTERMOST built-in step — there is no
	// separate middleware to install or to forget.
	//
	// The typed field prevents producers from forging observability IDs via
	// entry.Metadata["trace_id"] = "evil" — the two namespaces are now physically
	// separate, and Entry.Validate rejects ReservedMetadataKeys to keep producers
	// honest at write time.
	//
	// ref: OpenTelemetry SpanContext — typed carrier of trace identity, distinct
	// from application attributes (Baggage).
	Observability ObservabilityMetadata

	// FailurePolicy controls how an Emitter handles publisher-side failures
	// for this specific entry. Zero value (FailurePolicyDefault) falls
	// through to the Emitter's construction-time default.
	//
	// In-process control plane only — not marshaled to the wire envelope
	// (see MarshalEnvelope). Callers that need per-topic/per-event semantics
	// (e.g., security topics that must surface publisher errors; observability
	// topics that may drop on failure) set this at entry-construction time.
	//
	// ref: kubernetes apiserver/pkg/audit Backend.FailurePolicy (Ignore/Fail)
	// — policy lives with the event, not hardcoded per sink.
	FailurePolicy FailurePolicy `json:"-"`
}

// FailurePolicy expresses how an Emitter handles publisher-side failures
// for a particular Entry. Cells default their DirectEmitter to
// DirectPublishFailClosed (k8s apiserver audit model); individual entries
// opt into FailOpen for observational / non-critical sinks.
//
// Reserved for direct-publish paths: WriterEmitter (transactional outbox)
// surfaces errors through the surrounding transaction and does not consult
// FailurePolicy — the field is ignored there by design.
type FailurePolicy int

const (
	// FailurePolicyDefault falls through to Emitter ctor default. Zero value.
	FailurePolicyDefault FailurePolicy = iota
	// FailurePolicyFailOpen drops on publisher failure (log + counter), returns nil.
	FailurePolicyFailOpen
	// FailurePolicyFailClosed surfaces publisher failure to caller.
	FailurePolicyFailClosed
)

// Resolve returns the effective DirectPublishFailureMode, preferring the
// per-entry policy when set and falling through to the Emitter construction-
// time default when FailurePolicyDefault.
func (p FailurePolicy) Resolve(ctorDefault DirectPublishFailureMode) DirectPublishFailureMode {
	switch p {
	case FailurePolicyFailOpen:
		return DirectPublishFailOpen
	case FailurePolicyFailClosed:
		return DirectPublishFailClosed
	default:
		return ctorDefault
	}
}

// RoutingTopic returns the broker routing key for the entry.
// If Topic is set, it is returned; otherwise EventType is used as fallback.
func (e Entry) RoutingTopic() string {
	if e.Topic != "" {
		return e.Topic
	}
	return e.EventType
}

// Validate checks that required fields (ID, Topic or EventType, and Payload)
// are present, that Metadata is within size limits and free of reserved
// keys, and that Observability fields are well-formed. Writers MUST call
// Validate before persisting (every Writer.Write impl threads through this).
// (F-OB-03, META-SIZE-01, PR246-FU1 reserved-key invariant).
func (e Entry) Validate() error {
	if e.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: entry missing ID")
	}
	if e.RoutingTopic() == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: entry missing topic (Topic and EventType are both empty)")
	}
	if len(e.Payload) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "outbox: entry missing payload")
	}
	if len(e.Payload) > MaxPayloadBytes {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: payload size exceeds max",
			errcode.WithDetails(slog.Int("size", len(e.Payload)), slog.Int("max", MaxPayloadBytes)))
	}
	if err := validateMetadata(e.Metadata); err != nil {
		return err
	}
	if err := e.Observability.Validate(); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Writer / Relay / Publisher
// ---------------------------------------------------------------------------

// Writer writes outbox entries within a transaction. The implementation
// MUST ensure the outbox write is atomic with the business state write
// (same DB transaction).
type Writer interface {
	// Write persists an outbox entry atomically with the caller's business
	// state. Write MUST be invoked from within an active transaction; the
	// implementation extracts the tx from ctx via TxFromContext(ctx). Calling
	// Write outside of a persistence.TxRunner.RunInTx scope is a programming
	// error — implementations return an errcode error with KindInternal
	// (e.g. adapters/postgres returns ErrAdapterPGNoTx) rather than silently
	// writing without transactional guarantees.
	//
	// ref: nikolayk812/pgx-outbox writer.go -- explicit tx parameter +
	// ErrTxNil guard for the same MUST-have-tx contract.
	Write(ctx context.Context, entry Entry) error
}

// BatchWriter extends Writer with batch write support.
// Implementations that support batch operations SHOULD implement this
// interface for atomic multi-entry writes within a single transaction.
//
// If an implementation does not support BatchWriter, callers can use
// WriteBatchFallback which auto-detects batch support and falls back
// to sequential Write calls.
//
// ref: ThreeDotsLabs/watermill message/pubsub.go -- Publish(topic, ...msgs)
// variadic pattern. GoCell uses a separate interface instead to preserve
// the existing Writer contract and enable optimized multi-row INSERT.
type BatchWriter interface {
	Writer
	// WriteBatch persists multiple outbox entries atomically within the
	// caller's transaction scope. Implementations MUST validate entries
	// independently (defense-in-depth) even when called via
	// WriteBatchFallback, which only runs Entry.Validate(). If validation
	// fails for any entry, no entries are written.
	//
	// An empty entries slice is a no-op and returns nil.
	//
	// Implementations SHOULD use a single batch INSERT for efficiency.
	// The context MUST carry the caller's transaction (same requirement
	// as Writer.Write). All-or-nothing: either all entries are written
	// or none are (transaction rollback on failure).
	WriteBatch(ctx context.Context, entries []Entry) error
}

// WriteBatchFallback writes entries using the Writer interface, falling
// back to sequential Write calls if the writer does not implement
// BatchWriter.
//
// Validation scope: WriteBatchFallback runs Entry.Validate() on all
// entries upfront (topic + payload checks). Writer-specific validation
// (e.g. UUID format, transaction presence) is the responsibility of
// the Writer/BatchWriter implementation and may run independently.
//
// The caller MUST ensure ctx carries an active transaction. Atomicity
// depends on the transaction scope: if all writes happen within the
// same transaction, a failure rolls back everything. WriteBatchFallback
// itself does not manage transactions.
//
// An empty entries slice is a no-op and returns nil.
func WriteBatchFallback(ctx context.Context, w Writer, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	// Phase 1: Validate all entries upfront.
	for i, e := range entries {
		if err := e.Validate(); err != nil {
			return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"outbox: batch entry validation failed", err,
				errcode.WithDetails(slog.Int("entry_index", i)))
		}
	}

	// Phase 2: Use batch if available, otherwise sequential.
	if bw, ok := w.(BatchWriter); ok {
		return bw.WriteBatch(ctx, entries)
	}

	slog.Debug("outbox: WriteBatchFallback using sequential writes (writer does not implement BatchWriter)",
		slog.Int("count", len(entries)))
	for i, e := range entries {
		if err := w.Write(ctx, e); err != nil {
			return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"outbox: batch sequential write failed", err,
				errcode.WithDetails(
					slog.Int("entry_index", i),
					slog.String("entry_id", e.ID),
				))
		}
	}
	return nil
}

// Relay polls unpublished outbox entries and publishes them.
type Relay interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Publisher sends events to the message broker.
//
// ref: uber-go/fx app.go Lifecycle.Append OnStop(ctx) — ContextCloser pattern
// adopted so bootstrap teardown can share a unified shutCtx budget across all
// managed publishers.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	// Close terminates the publisher and releases resources. The ctx parameter
	// allows callers to share a shutdown budget (e.g., bootstrap shutCtx).
	Close(ctx context.Context) error
}

// NoopWriter is an explicit outbox writer sink for tests and demos.
// It validates entries like a real writer, then discards them instead of
// persisting anything. It is not a production durability mechanism.
//
// Use NoopWriter when:
//   - Unit testing Cells that require an outbox.Writer dependency
//   - Running demo/example code without a database
//
// NoopWriter still validates entries via Entry.Validate(), unlike a
// zero-value or nil writer. This catches schema errors during development.
type NoopWriter struct{}

// Write validates the entry, discards it, and returns nil.
func (NoopWriter) Write(_ context.Context, entry Entry) error {
	return entry.Validate()
}

// WriteBatch validates all entries, discards them, and returns nil.
func (NoopWriter) WriteBatch(_ context.Context, entries []Entry) error {
	for i, entry := range entries {
		if err := entry.Validate(); err != nil {
			return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"outbox: noop writer entry validation failed", err,
				errcode.WithDetails(slog.Int("entry_index", i)))
		}
	}
	return nil
}

var _ BatchWriter = NoopWriter{}

// Noop implements cell.Nooper. CheckNotNoop rejects NoopWriter in durable mode.
func (NoopWriter) Noop() bool { return true }

// DiscardPublisher is an explicit publisher sink for tests and demos.
// Unlike NoopWriter, it affects direct-publish flows rather than durable
// outbox writes. It is an explicit opt-in sink, not a default runtime fallback.
//
// Use DiscardPublisher when:
//   - Unit testing Cells that require an outbox.Publisher dependency
//   - Running demo/example code without a message broker
//
// Publish logs a structured warning and discards the payload. The warning
// ensures discard behavior is visible in logs.
//
// ref: go-logr zero-value safe (if sink == nil), slog DiscardHandler
type DiscardPublisher struct {
	// Logger is used for discard warnings. If nil, slog.Default() is used.
	Logger  *slog.Logger
	counter atomic.Uint64
}

// Close implements Publisher. DiscardPublisher holds no resources; always nil.
func (d *DiscardPublisher) Close(_ context.Context) error { return nil }

// Publish logs a discard warning, increments the counter, and returns nil.
// Safe to call on a nil receiver (typed nil defense).
func (d *DiscardPublisher) Publish(_ context.Context, topic string, _ []byte) error {
	if d == nil {
		slog.Default().Warn("outbox: discard publisher dropping message (nil receiver)",
			slog.String("topic", topic))
		return nil
	}
	d.counter.Add(1)
	l := d.Logger
	if l == nil {
		l = slog.Default()
	}
	l.Warn("outbox: discard publisher dropping message", slog.String("topic", topic))
	return nil
}

// DiscardCount returns the total number of messages discarded.
// Returns 0 on a nil receiver.
func (d *DiscardPublisher) DiscardCount() uint64 {
	if d == nil {
		return 0
	}
	return d.counter.Load()
}

var _ Publisher = (*DiscardPublisher)(nil)

// Noop implements cell.Nooper. CheckNotNoop rejects DiscardPublisher in durable mode.
func (*DiscardPublisher) Noop() bool { return true }

// isDiscardPublisher reports whether p is the explicit discard sink.
// Unexported: concrete-type detection should not leak into the public API.
// Cell/runtime code that needs discard awareness should use cell metadata
// or DurabilityMode instead of type-switching on Publisher implementations.
func isDiscardPublisher(p Publisher) bool {
	_, ok := p.(*DiscardPublisher)
	return ok
}

// ---------------------------------------------------------------------------
// Disposition / Receipt / HandleResult -- Solution B types
// ---------------------------------------------------------------------------

// Disposition describes the broker-level action for a consumed message.
//
//   - DispositionAck:     message processed successfully (or duplicate); broker may discard.
//   - DispositionRequeue: temporary failure / shutdown; broker should redeliver.
//   - DispositionReject:  permanent failure; broker routes to dead-letter exchange.
type Disposition uint8

const (
	// DispositionAck indicates the message was processed successfully (or is a
	// safe-to-skip duplicate); broker may discard.
	//
	// IMPORTANT: iota+1 ensures the zero value (0) is NOT a valid Disposition.
	// A forgotten/uninitialised HandleResult.Disposition will NOT silently ACK.
	DispositionAck     Disposition = iota + 1 // = 1
	DispositionRequeue                        // NACK+requeue -- transient / shutdown
	DispositionReject                         // NACK+no-requeue -- permanent failure -> DLX
)

// Valid reports whether d is a recognized Disposition value.
// The zero value is deliberately invalid to catch forgotten/uninitialised fields.
func (d Disposition) Valid() bool {
	return d >= DispositionAck && d <= DispositionReject
}

// String returns a human-readable label for the Disposition.
// The zero value returns "invalid" to surface forgotten/uninitialised fields.
func (d Disposition) String() string {
	switch d {
	case 0:
		return "invalid"
	case DispositionAck:
		return "ack"
	case DispositionRequeue:
		return "requeue"
	case DispositionReject:
		return "reject"
	default:
		return fmt.Sprintf("disposition(%d)", d)
	}
}

// SettlementResult describes whether the subscriber completed the final broker
// settlement action. It intentionally observes the boundary after Commit,
// Ack/Nack, and receipt release decisions, not the handler's process result.
type SettlementResult string

const (
	SettlementResultSuccess        SettlementResult = "success"
	SettlementResultRetryExhausted SettlementResult = "retry_exhausted"
	SettlementResultCommitFailed   SettlementResult = "commit_failed"
	SettlementResultAckFailed      SettlementResult = "ack_failed"
	SettlementResultNackFailed     SettlementResult = "nack_failed"
)

// SettlementObservation is emitted by subscribers after the final delivery
// settlement action is known.
type SettlementObservation struct {
	Entry         Entry
	Disposition   Disposition
	Result        SettlementResult
	ProcessReason string
	Err           error
}

// SettlementObserver records a post-settlement observation. Implementations
// must be non-blocking or bounded; subscriber delivery loops call observers on
// the hot path after broker settlement.
type SettlementObserver interface {
	ObserveSettlement(context.Context, SettlementObservation)
}

// SettlementObserverFunc adapts a function into a SettlementObserver.
type SettlementObserverFunc func(context.Context, SettlementObservation)

func (f SettlementObserverFunc) ObserveSettlement(ctx context.Context, obs SettlementObservation) {
	f(ctx, obs)
}

// HandleResult carries the business handler's processing outcome.
// The Subscriber inspects Disposition to decide Ack/Nack, then calls
// Settlement.Commit or Settlement.Release (received via SubscriberHandler
// return value) based on the broker outcome.
type HandleResult struct {
	Disposition Disposition
	Err         error // optional: logged/observed; nil for success

	// ProcessReason is a low-cardinality handler/process classification, such
	// as "ack", "stale", or "permanent_error". It is not a broker outcome.
	ProcessReason string

	// SettlementObservers are notified by subscribers after final broker
	// settlement. Middleware appends observers after ConsumerBase has resolved
	// retry/lease decisions.
	SettlementObservers []SettlementObserver
}

// NotifySettlement emits a settlement observation to every observer attached
// to the result. It is a no-op when no observer is present.
func NotifySettlement(
	ctx context.Context, result HandleResult, entry Entry,
	disposition Disposition, settlement SettlementResult, err error,
) {
	if len(result.SettlementObservers) == 0 {
		return
	}
	obs := SettlementObservation{
		Entry:         entry,
		Disposition:   disposition,
		Result:        settlement,
		ProcessReason: result.ProcessReason,
		Err:           err,
	}
	for _, observer := range result.SettlementObservers {
		if observer == nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.LogAttrs(ctx, slog.LevelError, "outbox: settlement observer panicked",
						slog.String("topic", entry.Topic),
						slog.String("entry_id", entry.ID),
						slog.Any("panic", redaction.RedactAny(r)))
				}
			}()
			observer.ObserveSettlement(ctx, obs)
		}()
	}
}

// EntryHandler is the business handler signature. Business handlers return a
// HandleResult that declares the intended broker disposition. Settlement is
// not visible to business handlers — it is delivered via SubscriberHandler,
// which is the Subscriber-layer interface (not business layer).
type EntryHandler func(context.Context, Entry) HandleResult

// SubscriberHandler is the Subscriber-layer handler type that ConsumerBase.Wrap
// returns and Subscriber.Subscribe accepts. It extends EntryHandler with a
// Settlement return value so Subscriber implementations can call
// Settlement.Commit before broker Ack and Settlement.Release after broker Nack
// without any idempotency types leaking into business code.
//
// Settlement may be nil when ConsumerBase has no idempotency state (fail-open
// claim error, ClaimDone, ClaimBusy short-circuit). Subscribers MUST nil-check
// before calling Commit/Release.
//
// Business handlers use EntryHandler — they never see Settlement. The type
// separation provides compile-time enforcement without archtest gates.
//
// ref: IBM/sarama consumer_group.go ConsumeClaim(session ConsumerGroupSession,
//
//	claim ConsumerGroupClaim) — settle handle as explicit method parameter
//
// ref: nats-io/nats.go jetstream/message.go Msg interface (Ack/Nak/Term)
type SubscriberHandler func(ctx context.Context, entry Entry) (HandleResult, Settlement)

// ---------------------------------------------------------------------------
// PermanentError -- error classification (domain concept)
// ---------------------------------------------------------------------------

// PermanentError wraps an error to indicate it should not be retried
// and should be routed to the dead-letter queue. This is a domain concept
// alongside Disposition and HandleResult.
//
// ref: Temporal SDK temporal.ApplicationError (NonRetryable flag in SDK core);
// Watermill delegates error classification to middleware -- GoCell makes it
// explicit at the kernel level so WrapLegacyHandler and InMemoryEventBus
// can detect it without depending on adapter-layer types.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	if e.Err == nil {
		return "permanent: <nil>"
	}
	return fmt.Sprintf("permanent: %s", e.Err.Error())
}

func (e *PermanentError) Unwrap() error {
	return e.Err
}

// NewPermanentError wraps an error as a PermanentError.
func NewPermanentError(err error) *PermanentError {
	return &PermanentError{Err: err}
}

// ---------------------------------------------------------------------------
// Subscriber
// ---------------------------------------------------------------------------

// Subscriber consumes events from a topic.
//
// ref: ThreeDotsLabs/watermill message/pubsub.go Subscriber interface
// Adopted: Close() for clean shutdown; topic-based subscription model.
// Deviated: callback-based EntryHandler instead of channel-based (<-chan *Message)
// to align with GoCell's ConsumerBase pattern and simplify consumer lifecycle.
// Extended: Setup/Ready split mirrors Watermill Router's setup-before-run pattern,
// eliminating the 500ms startup-timeout heuristic in eventrouter (Commit 3).
//
// ref: Kafka sarama ConsumerGroup -- consumerGroup isolates consumption; same group
// competes, different groups each get a full copy (fanout).
// ref: go-micro broker.SubscribeOptions.Queue -- same concept, different name.
type Subscriber interface {
	// Setup pre-declares broker topology (exchanges, queues, bindings) for the
	// given subscription before Subscribe is called. Callers SHOULD await Ready
	// before publishing to ensure messages are queued deterministically.
	// In-memory implementations MUST return nil immediately.
	Setup(ctx context.Context, sub Subscription) error

	// Ready returns a channel that is closed when the subscription is ready to
	// consume. In-memory implementations SHOULD return an already-closed channel.
	Ready(sub Subscription) <-chan struct{}

	// Subscribe registers a handler for the given subscription and blocks until
	// ctx is canceled or an unrecoverable error occurs.
	//
	// Subscription.ConsumerGroup identifies the logical consumer group.
	// Subscribers sharing the same group compete for messages (load-balanced);
	// different groups each receive a full copy (fanout).
	//
	// handler is SubscriberHandler so the Subscriber can receive Settlement
	// alongside HandleResult without idempotency types leaking into business
	// code. Callers that hold an EntryHandler and want the full business
	// pipeline (middleware chain + ConsumerBase idempotency) should use
	// SubscriberWithMiddleware.SubscribeEntry instead of lifting manually.
	Subscribe(ctx context.Context, sub Subscription, handler SubscriberHandler) error

	// Close terminates all active subscriptions and releases resources.
	// The ctx parameter allows callers to share a shutdown budget.
	//
	// ref: uber-go/fx app.go Lifecycle.Append OnStop(ctx context.Context) error
	// ref: ThreeDotsLabs/watermill message/pubsub.go Subscriber.Close()
	Close(ctx context.Context) error
}

// SubscriberIntakeStopper is a Subscriber-implementer extension contract.
// Business handlers should never reference this interface.
//
// Subscribers that implement it can stop accepting new deliveries while still
// processing in-flight ones, enabling a two-phase drain during shutdown
// (stop intake → wait in-flight handlers). Router.Close calls StopIntake on
// subscribers implementing this interface before canceling the run context.
//
// Subscribers that do not implement this interface will fall back to the
// legacy single-phase close behavior (cancel context only).
//
// StopIntake MUST be idempotent. It SHOULD be safe to call concurrently with
// in-flight Subscribe calls.
//
// Errors are best-effort: callers (e.g., eventrouter.Router.Close) log a warning
// and proceed to context cancellation regardless. Return an error only for
// caller-observable failures that operators would want to see in logs.
//
// ref: ThreeDotsLabs/watermill message/router.go (closingInProgressCh two-phase barrier)
// ref: uber-go/fx app.go shutdown semantics (run ctx vs stop ctx separation)
type SubscriberIntakeStopper interface {
	StopIntake(ctx context.Context) error
}

// SubscriberWithMiddleware wraps a Subscriber with a business middleware chain
// and a required ConsumerBase for idempotency/retry.
//
// The SubscribeEntry method is the primary entry point for callers that hold
// an EntryHandler (e.g., eventrouter.Router). It orchestrates:
//
//  1. Business middleware chain (EntryHandler → EntryHandler): applied via
//     slices.Backward(s.Middleware) — [0] is outermost (first to see each
//     delivery, last to return), [len-1] is innermost (adjacent to
//     ConsumerBase). This is the opposite of chi/Kratos forward composition
//     where [0] is applied last.
//  2. ConsumerBase.Wrap (EntryHandler → SubscriberHandler): injects
//     idempotency Claim/Commit/Release and retry logic. SubscribeEntry fails
//     fast when ConsumerBase is nil.
//  3. Observability restore (entry.Observability → ctx): built-in OUTERMOST
//     wrapper applied inside Inner.Subscribe so every layer sees a ctx
//     populated with trace_id/request_id/correlation_id.
//  4. Inner.Subscribe: the actual broker subscription.
//
// Observability restoration is a paired invariant with Entry.InjectObservability
// FromContext on the producer side — there is no kill-switch. Raw delivery
// without restore (integration testing only) should invoke Inner.Subscribe
// directly.
//
// SubscriberWithMiddleware does NOT implement the Subscriber interface: it
// exposes only SubscribeEntry (EntryHandler) as the single public entry point.
// This prevents the lift→discard footgun where callers could assign
// *SubscriberWithMiddleware to outbox.Subscriber and bypass the business
// middleware chain. Adapter-layer tests that need raw SubscriberHandler delivery
// must call the inner subscriber directly.
//
// Construction is funneled through NewSubscriberWithMiddleware: fields are
// unexported so struct literals cannot create an invalid (nil-inner /
// nil-ConsumerBase) instance. This mirrors the OUTBOX-SERVICE-01 ctor
// fail-fast pattern that 12 outbox-bound services already follow.
//
// ref: ThreeDotsLabs/watermill message/router.go — handleMessage applies
// middleware then calls handler; Ack/Nack monopoly stays in router.
// ref: go-kratos/kratos — middleware chain on business Request/Reply;
// transport/grpc monopolizes gRPC status.
// ref: IBM/sarama consumer_group.go — ConsumeClaim owns MarkMessage;
// business handler in ConsumeClaim body never calls MarkMessage directly.
type SubscriberWithMiddleware struct {
	inner        Subscriber
	middleware   []SubscriptionMiddleware
	consumerBase *ConsumerBase
}

// NewSubscriberWithMiddleware constructs a SubscriberWithMiddleware. inner and
// cb are required (nil → error); mw is optional. The constructor is the only
// path to a valid instance — fields are unexported so a struct literal cannot
// bypass the nil-guards. Methods (Setup / Ready / SubscribeEntry / Close /
// StopIntake) therefore delegate without runtime nil checks; the boundary is
// closed at construction time.
func NewSubscriberWithMiddleware(inner Subscriber, cb *ConsumerBase, mw ...SubscriptionMiddleware) (*SubscriberWithMiddleware, error) {
	if inner == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"outbox: NewSubscriberWithMiddleware requires non-nil inner Subscriber")
	}
	if cb == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"outbox: NewSubscriberWithMiddleware requires non-nil ConsumerBase")
	}
	return &SubscriberWithMiddleware{inner: inner, consumerBase: cb, middleware: mw}, nil
}

// Setup delegates topology pre-declaration to the inner subscriber.
func (s *SubscriberWithMiddleware) Setup(ctx context.Context, sub Subscription) error {
	return s.inner.Setup(ctx, sub)
}

// Ready delegates to the inner subscriber.
func (s *SubscriberWithMiddleware) Ready(sub Subscription) <-chan struct{} {
	return s.inner.Ready(sub)
}

// SubscribeEntry is the entry point for callers that hold an EntryHandler
// (e.g. eventrouter.Router). It applies the full pipeline:
// business middleware chain → ConsumerBase.Wrap → observability restore → inner Subscribe.
//
// The business middleware chain is applied via slices.Backward(s.middleware):
// middleware[0] is the outermost layer (first to wrap, last to return) and
// middleware[len-1] is the innermost layer (adjacent to ConsumerBase). This is
// the opposite of chi/Kratos forward composition where index 0 is applied last.
// Example with middleware = [A, B, C]: execution order is A → B → C → handler.
//
// This method is intentionally not part of the Subscriber interface: it accepts
// the business-layer EntryHandler rather than the framework-layer SubscriberHandler,
// enforcing the boundary at the type level. eventrouter.Router calls SubscribeEntry
// directly; Subscriber adapters (rabbitmq, eventbus) implement Subscribe directly.
func (s *SubscriberWithMiddleware) SubscribeEntry(ctx context.Context, sub Subscription, h EntryHandler) error {
	if err := sub.Validate(); err != nil {
		// fmt.Errorf with %w is intentional here: sub.Validate() already
		// returns *errcode.Error with full Code/Details/Internal layering;
		// re-wrapping via errcode.Wrap would erase the inner Code at the
		// outer layer, forcing callers to errors.As twice. Adding a const
		// context prefix via %w keeps the cause discoverable while making
		// the operation site visible in the error chain.
		return fmt.Errorf("outbox: SubscriberWithMiddleware: %w", err)
	}

	// Step 1: apply business middleware chain (EntryHandler → EntryHandler).
	wrapped := h
	for _, mw := range slices.Backward(s.middleware) {
		wrapped = mw(sub, wrapped)
	}

	// Step 2: ConsumerBase converts EntryHandler → SubscriberHandler, injecting
	// idempotency Settlement.
	subHandler := s.consumerBase.Wrap(sub, wrapped)

	// Step 3: observability restore — built-in OUTERMOST wrapper so all layers
	// (business middleware, ConsumerBase, inner Subscribe) see a populated ctx.
	withRestore := func(reqCtx context.Context, entry Entry) (HandleResult, Settlement) {
		return subHandler(entry.Observability.RestoreToContext(reqCtx), entry)
	}
	return s.inner.Subscribe(ctx, sub, withRestore)
}

// Close delegates to the inner subscriber, forwarding the ctx unchanged so
// the inner implementation can honor the shutdown budget.
func (s *SubscriberWithMiddleware) Close(ctx context.Context) error {
	return s.inner.Close(ctx)
}

// StopIntake forwards to the inner subscriber if it implements
// SubscriberIntakeStopper. Returns nil if it does not (graceful degradation).
// Safe to call multiple times (idempotent, assuming inner is).
func (s *SubscriberWithMiddleware) StopIntake(ctx context.Context) error {
	stopper, ok := s.inner.(SubscriberIntakeStopper)
	if !ok {
		return nil
	}
	return stopper.StopIntake(ctx)
}
