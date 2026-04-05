package rabbitmq

import (
	"context"
	"encoding/json"
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

	// IdempotencyTTL is the TTL for idempotency keys.
	// Default: 24h (idempotency.DefaultTTL).
	IdempotencyTTL time.Duration

	// DLQTopic is the dead-letter topic name. If empty, defaults to "{topic}.dlq".
	DLQTopic string
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

// ConsumerBase wraps an outbox.Entry handler with idempotency checking,
// exponential backoff retry, and dead-letter queue routing.
//
// Consumer: cg-{ConsumerGroup}-{topic}
// Idempotency key: {ConsumerGroup}:{event-id}, TTL 24h
// ACK timing: after business logic + idempotency key written
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
type ConsumerBase struct {
	checker   idempotency.Checker
	publisher outbox.Publisher
	config    ConsumerBaseConfig
}

// NewConsumerBase creates a ConsumerBase.
//
// checker: idempotency.Checker implementation (e.g., from redis adapter).
// publisher: outbox.Publisher for routing dead-letter messages.
func NewConsumerBase(checker idempotency.Checker, publisher outbox.Publisher, config ConsumerBaseConfig) *ConsumerBase {
	config.setDefaults()
	return &ConsumerBase{
		checker:   checker,
		publisher: publisher,
		config:    config,
	}
}

// Wrap returns a handler function that wraps the given business handler with
// idempotency checking, retry with exponential backoff, and DLQ routing.
//
// The returned handler is suitable for use with Subscriber.Subscribe().
//
// Rules:
//   - return nil -> ACK (business logic succeeded, idempotency key written)
//   - return error -> NACK + exponential backoff retry (transient error)
//   - return PermanentError -> dead letter (no retry)
//   - unmarshal failure in Subscriber -> dead letter (see subscriber.go)
func (cb *ConsumerBase) Wrap(topic string, handler func(context.Context, outbox.Entry) error) func(context.Context, outbox.Entry) error {
	return func(ctx context.Context, entry outbox.Entry) error {
		idempotencyKey := fmt.Sprintf("%s:%s", cb.config.ConsumerGroup, entry.ID)

		// Check idempotency.
		processed, err := cb.checker.IsProcessed(ctx, idempotencyKey)
		if err != nil {
			slog.Warn("rabbitmq: idempotency check failed, proceeding with handler",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup),
				slog.String("error", err.Error()))
		}
		if processed {
			slog.Debug("rabbitmq: event already processed, skipping",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup))
			return nil
		}

		// Execute handler with retry.
		var lastErr error
		for attempt := range cb.config.RetryCount {
			lastErr = handler(ctx, entry)
			if lastErr == nil {
				// Handler succeeded — mark as processed.
				if markErr := cb.checker.MarkProcessed(ctx, idempotencyKey, cb.config.IdempotencyTTL); markErr != nil {
					slog.Error("rabbitmq: failed to mark event as processed",
						slog.String("event_id", entry.ID),
						slog.String("topic", topic),
						slog.String("consumer_group", cb.config.ConsumerGroup),
						slog.String("error", markErr.Error()))
				}
				return nil
			}

			// Check if this is a permanent error.
			if _, ok := lastErr.(*PermanentError); ok {
				slog.Warn("rabbitmq: permanent error, routing to DLQ",
					slog.String("event_id", entry.ID),
					slog.String("topic", topic),
					slog.String("consumer_group", cb.config.ConsumerGroup),
					slog.String("error", lastErr.Error()))
				cb.deadLetter(ctx, topic, entry, lastErr, attempt+1)
				return nil // Return nil to ACK the original message.
			}

			// Transient error — backoff before retry.
			if attempt < cb.config.RetryCount-1 {
				delay := cb.config.RetryBaseDelay * (1 << attempt)
				slog.Warn("rabbitmq: transient error, retrying",
					slog.String("event_id", entry.ID),
					slog.String("topic", topic),
					slog.Int("attempt", attempt+1),
					slog.Int("max_retries", cb.config.RetryCount),
					slog.Duration("backoff", delay),
					slog.String("error", lastErr.Error()))

				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}

		// Exhausted all retries — route to DLQ.
		slog.Error("rabbitmq: retry budget exhausted, routing to DLQ",
			slog.String("event_id", entry.ID),
			slog.String("topic", topic),
			slog.String("consumer_group", cb.config.ConsumerGroup),
			slog.Int("retry_count", cb.config.RetryCount),
			slog.String("error", lastErr.Error()))
		cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount)

		// Return nil to ACK the original message (it's been DLQ'd).
		return nil
	}
}

// deadLetter routes a failed message to the dead-letter queue.
// It publishes the original entry with x-death metadata to the DLQ topic.
func (cb *ConsumerBase) deadLetter(ctx context.Context, topic string, entry outbox.Entry, originalErr error, retryCount int) {
	dlqTopic := cb.config.DLQTopic
	if dlqTopic == "" {
		dlqTopic = topic + ".dlq"
	}

	// Enrich metadata with death info.
	dlqEntry := entry
	if dlqEntry.Metadata == nil {
		dlqEntry.Metadata = make(map[string]string)
	}
	dlqEntry.Metadata["x-death-reason"] = originalErr.Error()
	dlqEntry.Metadata["x-death-topic"] = topic
	dlqEntry.Metadata["x-death-consumer-group"] = cb.config.ConsumerGroup
	dlqEntry.Metadata["x-death-retry-count"] = fmt.Sprintf("%d", retryCount)
	dlqEntry.Metadata["x-death-time"] = time.Now().UTC().Format(time.RFC3339)

	payload, err := json.Marshal(dlqEntry)
	if err != nil {
		slog.Error("rabbitmq: failed to marshal DLQ entry",
			slog.String("event_id", entry.ID),
			slog.String("topic", topic),
			slog.String("error", err.Error()))
		return
	}

	if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
		slog.Error("rabbitmq: failed to publish to DLQ",
			slog.String("event_id", entry.ID),
			slog.String("topic", topic),
			slog.String("dlq_topic", dlqTopic),
			slog.String("error", err.Error()),
			slog.Int("retry_count", retryCount))
		return
	}

	// T25: DLQ observability — log every dead-letter routing.
	slog.Error("rabbitmq: message routed to dead letter queue",
		slog.String("event_id", entry.ID),
		slog.String("topic", topic),
		slog.String("dlq_topic", dlqTopic),
		slog.String("consumer_group", cb.config.ConsumerGroup),
		slog.String("error", originalErr.Error()),
		slog.Int("retry_count", retryCount))
}
