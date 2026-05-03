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

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Metadata size limits (META-SIZE-01)
//
// These constants prevent unbounded metadata from degrading broker throughput
// or exceeding transport-level control-line limits.
//
// ref: OTel sdk/trace/span_limits.go -- 128 attributes/span (GoCell uses 64
//      as a tighter balance between overhead prevention and practical use)
// ref: NATS server/const.go -- MAX_CONTROL_LINE_SIZE = 4096 bytes
// ref: RabbitMQ -- no hard header-size limit, but 64 KB total is a pragmatic
//      ceiling aligned with most broker implementations
// ---------------------------------------------------------------------------

const (
	// MaxMetadataKeys is the maximum number of key-value pairs in Entry.Metadata.
	// Typical GoCell entries carry 3-10 keys (trace_id, request_id, correlation_id
	// plus domain context); 64 provides 6x headroom while keeping serialized
	// overhead under 1 KB for small entries. OTel allows 128 attributes/span.
	MaxMetadataKeys = 64

	// MaxMetadataKeyLen is the maximum byte length of a single metadata key.
	// Measured in bytes (len()), not runes -- multi-byte UTF-8 keys are counted
	// by their wire size, consistent with transport-level limits.
	MaxMetadataKeyLen = 256

	// MaxMetadataValueLen is the maximum byte length of a single metadata value.
	// Aligned with NATS MAX_CONTROL_LINE_SIZE (4096). Measured in bytes.
	MaxMetadataValueLen = 4096

	// MaxMetadataTotalSize is the maximum total byte size of all metadata
	// key-value pairs combined (sum of len(k)+len(v) for each pair).
	MaxMetadataTotalSize = 65536
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

// validateMetadata checks metadata map against size limits and rejects any
// keys claimed by the kernel observability bridge (ReservedMetadataKeys).
// nil or empty metadata is valid (no checks needed).
func validateMetadata(m map[string]string) error {
	if len(m) == 0 {
		return nil
	}
	if len(m) > MaxMetadataKeys {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("outbox: metadata key count %d exceeds max %d", len(m), MaxMetadataKeys))
	}
	var total int
	for k, v := range m {
		if _, reserved := reservedMetadataKeySet[k]; reserved {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("outbox: metadata key %q is reserved for the observability bridge — use Entry.Observability instead", k))
		}
		if len(k) > MaxMetadataKeyLen {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("outbox: metadata key length %d exceeds max %d (key=%q)", len(k), MaxMetadataKeyLen, truncate(k, 64)))
		}
		if len(v) > MaxMetadataValueLen {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("outbox: metadata value length %d exceeds max %d (key=%q)", len(v), MaxMetadataValueLen, truncate(k, 64)))
		}
		total += len(k) + len(v)
	}
	if total > MaxMetadataTotalSize {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("outbox: metadata total size %d exceeds max %d", total, MaxMetadataTotalSize))
	}
	return nil
}

// truncate returns the first n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
		return errcode.New(errcode.ErrValidationFailed, "outbox: entry missing ID")
	}
	if e.RoutingTopic() == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox: entry missing topic (Topic and EventType are both empty)")
	}
	if len(e.Payload) == 0 {
		return errcode.New(errcode.ErrValidationFailed, "outbox: entry missing payload")
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

// Writer writes outbox entries within a transaction.
// The implementation must ensure the outbox write is atomic with the
// business state write (same DB transaction).
type Writer interface {
	// Write persists an outbox entry atomically with the caller's business state.
	// Implementations that require transactional guarantees SHOULD use a
	// context-embedded transaction pattern (e.g., extract tx from context via
	// TxFromContext(ctx)) to participate in the caller's transaction scope.
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
			return fmt.Errorf("outbox: entry[%d]: %w", i, err)
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
			return fmt.Errorf("outbox: write entry[%d] (id=%s): %w", i, e.ID, err)
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
			return fmt.Errorf("outbox: noop writer entry[%d]: %w", i, err)
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
// Receipt.Commit or Receipt.Release based on the broker outcome.
type HandleResult struct {
	Disposition Disposition
	Err         error // optional: logged/observed; nil for success

	// Receipt is the Subscriber-implementer-internal channel for delivery loop
	// Commit/Release after broker Ack/Nack. Business handlers MUST NOT read or
	// write this field — reading observes an unspecified intermediate state,
	// writing silently overwrites the ConsumerBase-set value and breaks
	// idempotency Commit/Release. The HANDLER-RECEIPT-WRITE-01 archtest rule
	// statically enforces the no-write half. See 029 #12 (PR-V1-OUTBOX-RECEIPT-EXTRACT)
	// for v2 extraction roadmap.
	Receipt idempotency.Receipt

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
						slog.Any("panic", r))
				}
			}()
			observer.ObserveSettlement(ctx, obs)
		}()
	}
}

// EntryHandler is the Solution B handler signature. Business handlers return
// a HandleResult that declares the intended broker disposition. The Receipt
// field is a Subscriber-implementer hand-off; see HandleResult godoc.
type EntryHandler func(context.Context, Entry) HandleResult

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
	Subscribe(ctx context.Context, sub Subscription, handler EntryHandler) error

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

// SubscriberWithMiddleware wraps a Subscriber so that every handler passed to
// Subscribe is first wrapped by the SubscriptionMiddleware chain.
// Middleware is applied in order: [0] is outermost, [len-1] is innermost.
//
// Observability context restoration (entry.Observability →
// ctxkeys.Trace*/Request*/CorrelationID) is built into Subscribe as the
// OUTERMOST wrapper — it always runs before any user middleware so the
// rest of the chain sees a context populated with the originating
// trace/request identity. There is no kill-switch: the producer-side
// Entry.InjectObservabilityFromContext and the consumer-side restoration
// here are paired invariants. If a caller intentionally needs raw delivery
// without restore (rare, integration testing only), they construct
// SubscriberWithMiddleware directly is not the right tool — they should
// invoke Subscriber.Subscribe on the inner subscriber.
type SubscriberWithMiddleware struct {
	Inner      Subscriber
	Middleware []SubscriptionMiddleware
}

// Compile-time interface check.
var _ Subscriber = (*SubscriberWithMiddleware)(nil)

// Setup delegates topology pre-declaration to Inner.
func (s *SubscriberWithMiddleware) Setup(ctx context.Context, sub Subscription) error {
	return s.Inner.Setup(ctx, sub)
}

// Ready delegates to Inner.
func (s *SubscriberWithMiddleware) Ready(sub Subscription) <-chan struct{} {
	return s.Inner.Ready(sub)
}

// Subscribe wraps the handler with the middleware chain (Subscription is
// passed to each middleware), then delegates to Inner. Observability
// context restoration is the OUTERMOST wrapper — entry.Observability is
// applied to ctx before any user middleware sees it. Restoration is
// idempotent (existing non-empty ctx values win) and zero-struct safe
// (no fields set ⇒ no-op).
func (s *SubscriberWithMiddleware) Subscribe(ctx context.Context, sub Subscription, handler EntryHandler) error {
	wrapped := handler
	for _, v := range slices.Backward(s.Middleware) {
		wrapped = v(sub, wrapped)
	}
	withRestore := func(reqCtx context.Context, entry Entry) HandleResult {
		return wrapped(entry.Observability.RestoreToContext(reqCtx), entry)
	}
	return s.Inner.Subscribe(ctx, sub, withRestore)
}

// Close delegates to the inner subscriber, forwarding the ctx unchanged so
// the inner implementation can honor the shutdown budget.
func (s *SubscriberWithMiddleware) Close(ctx context.Context) error {
	return s.Inner.Close(ctx)
}

// StopIntake forwards to Inner if it implements SubscriberIntakeStopper.
// Returns nil if Inner does not implement the optional interface (graceful
// degradation). Safe to call multiple times (idempotent, assuming Inner is).
func (s *SubscriberWithMiddleware) StopIntake(ctx context.Context) error {
	stopper, ok := s.Inner.(SubscriberIntakeStopper)
	if !ok {
		return nil
	}
	return stopper.StopIntake(ctx)
}
