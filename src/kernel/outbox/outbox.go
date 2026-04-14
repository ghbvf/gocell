// Package outbox defines interfaces for the transactional outbox pattern.
// Implementations live in adapters/ (e.g., adapters/postgres).
//
// ref: ThreeDotsLabs/watermill message/ — Message 統一模型, Publisher/Subscriber 接口
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Entry
// ---------------------------------------------------------------------------

// Entry represents a single outbox record to be published.
type Entry struct {
	// ID is the canonical idempotency identifier. Consumers SHOULD use this
	// field to construct idempotency keys.
	ID string
	AggregateID   string
	AggregateType string
	EventType     string
	Topic         string // broker routing key; falls back to EventType if empty
	Payload       []byte
	CreatedAt     time.Time
	// Metadata carries optional message metadata (ref: Watermill Message.Metadata).
	//
	// Reserved observability keys:
	//   - trace_id
	//   - request_id
	//   - correlation_id
	//
	// Writer-side bridges may fill missing reserved keys from context before
	// persistence. Consumer-side middleware may restore those keys back into
	// handler context. Explicit non-empty metadata values win over bridge values.
	Metadata map[string]string
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
// are present. Writers SHOULD call Validate before persisting. (F-OB-03)
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
// ref: ThreeDotsLabs/watermill message/pubsub.go — Publish(topic, ...msgs)
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
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
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

// DiscardPublisher is an explicit publisher sink for tests and demos.
// Unlike NoopWriter, it affects direct-publish flows rather than durable
// outbox writes. It is an explicit opt-in sink, not a default runtime fallback.
//
// Use DiscardPublisher when:
//   - Unit testing Cells that require an outbox.Publisher dependency
//   - Running demo/example code without a message broker
//
// Publish logs a structured warning via slog.Default() and discards the
// payload. The warning ensures discard behavior is visible in logs.
type DiscardPublisher struct{}

// Publish logs a discard warning and returns nil.
func (DiscardPublisher) Publish(_ context.Context, topic string, _ []byte) error {
	slog.Warn("outbox: discard publisher dropping message", slog.String("topic", topic))
	return nil
}

var _ Publisher = DiscardPublisher{}

// isDiscardPublisher reports whether p is the explicit discard sink.
// Unexported: concrete-type detection should not leak into the public API.
// Cell/runtime code that needs discard awareness should use cell metadata
// or DurabilityMode instead of type-switching on Publisher implementations.
func isDiscardPublisher(p Publisher) bool {
	switch p.(type) {
	case DiscardPublisher, *DiscardPublisher:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Disposition / Receipt / HandleResult — Solution B types
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
	DispositionRequeue                        // NACK+requeue — transient / shutdown
	DispositionReject                         // NACK+no-requeue — permanent failure → DLX
)

// Valid reports whether d is a recognised Disposition value.
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

// Deprecated: Use idempotency.Receipt directly. This alias will be removed
// after all callers migrate (target: Sprint N+2, per project deprecation policy).
// Canonical ownership now lives in kernel/idempotency.
type Receipt = idempotency.Receipt

// HandleResult carries the business handler's processing outcome.
// The Subscriber inspects Disposition to decide Ack/Nack, then calls
// Receipt.Commit or Receipt.Release based on the broker outcome.
type HandleResult struct {
	Disposition Disposition
	Err         error   // optional: logged/observed; nil for success
	Receipt     Receipt // nil when idempotency is not in use
}

// EntryHandler is the Solution B handler signature. Business handlers return
// a HandleResult that declares the intended broker disposition and carries an
// optional idempotency Receipt.
type EntryHandler func(context.Context, Entry) HandleResult

// ---------------------------------------------------------------------------
// PermanentError — error classification (domain concept)
// ---------------------------------------------------------------------------

// PermanentError wraps an error to indicate it should not be retried
// and should be routed to the dead-letter queue. This is a domain concept
// alongside Disposition and HandleResult.
//
// ref: Temporal SDK temporal.ApplicationError (NonRetryable flag in SDK core);
// Watermill delegates error classification to middleware — GoCell makes it
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
// Legacy compatibility
// ---------------------------------------------------------------------------

// LegacyHandler is the pre-Solution-B handler signature kept for reference.
// New code should use EntryHandler.
type LegacyHandler = func(context.Context, Entry) error

// WrapLegacyHandler adapts a LegacyHandler to the new EntryHandler contract:
//   - nil error         → DispositionAck
//   - PermanentError    → DispositionReject (routed to DLX)
//   - other non-nil err → DispositionRequeue (transient by default)
//
// This allows existing cell handlers to compile against the new Subscriber
// interface without immediate rewrite.
func WrapLegacyHandler(fn LegacyHandler) EntryHandler {
	return func(ctx context.Context, entry Entry) HandleResult {
		if err := fn(ctx, entry); err != nil {
			var permErr *PermanentError
			if errors.As(err, &permErr) {
				return HandleResult{Disposition: DispositionReject, Err: err}
			}
			return HandleResult{Disposition: DispositionRequeue, Err: err}
		}
		return HandleResult{Disposition: DispositionAck}
	}
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
//
// ref: Kafka sarama ConsumerGroup — consumerGroup isolates consumption; same group
// competes, different groups each get a full copy (fanout).
// ref: go-micro broker.SubscribeOptions.Queue — same concept, different name.
type Subscriber interface {
	// Subscribe registers a handler for the given topic. The handler is called
	// for each incoming entry and returns a HandleResult that declares the
	// intended broker disposition.
	//
	// consumerGroup identifies the logical consumer group. Subscribers in
	// the same group compete for messages (load-balanced); different groups
	// each receive a full copy (fanout).
	//
	// Empty consumerGroup is accepted for backward compatibility but its
	// semantics are backend-specific and NOT portable:
	//   - InMemoryEventBus: broadcast to all subscribers (fanout)
	//   - RabbitMQ: falls back to topic-named queue (competing)
	// Cell code SHOULD always pass a non-empty group via EventRouter.AddHandler.
	//
	// Subscribe blocks until ctx is cancelled or an unrecoverable error occurs.
	Subscribe(ctx context.Context, topic string, handler EntryHandler, consumerGroup string) error

	// Close terminates all active subscriptions and releases resources.
	Close() error
}

// TopicHandlerMiddleware transforms an EntryHandler, receiving the topic name.
// It is the event-consumer analogue of HTTP middleware.
type TopicHandlerMiddleware func(topic string, next EntryHandler) EntryHandler

// SubscriberWithMiddleware wraps a Subscriber so that every handler passed
// to Subscribe is first wrapped by the given middleware chain.
// Middleware is applied in order: [0] is outermost, [len-1] is innermost.
type SubscriberWithMiddleware struct {
	Inner      Subscriber
	Middleware []TopicHandlerMiddleware
}

// Compile-time interface check.
var _ Subscriber = (*SubscriberWithMiddleware)(nil)

// Subscribe wraps the handler with the middleware chain, then delegates to Inner.
func (s *SubscriberWithMiddleware) Subscribe(ctx context.Context, topic string, handler EntryHandler, consumerGroup string) error {
	wrapped := handler
	for i := len(s.Middleware) - 1; i >= 0; i-- {
		wrapped = s.Middleware[i](topic, wrapped)
	}
	return s.Inner.Subscribe(ctx, topic, wrapped, consumerGroup)
}

// Close delegates to the inner subscriber.
func (s *SubscriberWithMiddleware) Close() error {
	return s.Inner.Close()
}
