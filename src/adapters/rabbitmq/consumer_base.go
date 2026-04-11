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

	// IdempotencyTTL is the TTL for idempotency keys.
	// Default: 24h (idempotency.DefaultTTL).
	IdempotencyTTL time.Duration
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

// ConsumerBase wraps an outbox.EntryHandler with idempotency checking and
// exponential backoff retry. DLQ routing is now handled by the broker via
// DLX (DispositionReject triggers Nack requeue=false).
//
// Consumer: cg-{ConsumerGroup}-{topic}
// Idempotency key: {ConsumerGroup}:{event-id}, TTL 24h
// ACK timing: after business logic returns DispositionAck
// Retry: transient errors -> retry+backoff / permanent errors -> DispositionReject → DLX
type ConsumerBase struct {
	checker idempotency.Checker
	config  ConsumerBaseConfig
}

// NewConsumerBase creates a ConsumerBase.
//
// checker: idempotency.Checker implementation (e.g., from redis adapter).
func NewConsumerBase(checker idempotency.Checker, config ConsumerBaseConfig) *ConsumerBase {
	config.setDefaults()
	return &ConsumerBase{
		checker: checker,
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
// Solution B semantics:
//   - ConsumerBase only determines the "business intent" (Ack/Reject/Requeue).
//   - The broker-layer Subscriber performs the actual Ack/Nack and Receipt lifecycle.
//   - DLQ routing is now broker-native via DLX (Nack requeue=false), not application-side publish.
//
// Rules:
//   - handler returns DispositionAck → pass through as Ack
//   - handler returns DispositionRequeue → pass through as Requeue
//   - handler returns DispositionReject → pass through as Reject
//   - handler returns error with non-Ack disposition → retry with backoff
//   - PermanentError → Reject (broker routes to DLX)
//   - retry budget exhausted → Reject
//   - ctx cancelled / shutdown → Requeue (release lease)
func (cb *ConsumerBase) Wrap(topic string, handler outbox.EntryHandler) outbox.EntryHandler {
	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		idempotencyKey := fmt.Sprintf("%s:%s", cb.config.ConsumerGroup, entry.ID)

		// Atomic idempotency check-and-mark via TryProcess (eliminates TOCTOU race).
		shouldProcess, err := cb.checker.TryProcess(ctx, idempotencyKey, cb.config.IdempotencyTTL)
		if err != nil {
			slog.Warn("rabbitmq: idempotency check failed, proceeding with handler",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup),
				slog.String("error", err.Error()))
			// On error, default to processing (fail-open) to avoid dropping messages.
			shouldProcess = true
		}
		if !shouldProcess {
			slog.Debug("rabbitmq: event already processed, skipping",
				slog.String("event_id", entry.ID),
				slog.String("topic", topic),
				slog.String("consumer_group", cb.config.ConsumerGroup))
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}

		// Execute handler with retry.
		var lastResult outbox.HandleResult
		for attempt := range cb.config.RetryCount {
			lastResult = handler(ctx, entry)
			if lastResult.Disposition == outbox.DispositionAck {
				return lastResult
			}

			// Check if this is a permanent error / explicit rejection.
			var permErr *PermanentError
			if lastResult.Disposition == outbox.DispositionReject ||
				(lastResult.Err != nil && errors.As(lastResult.Err, &permErr)) {
				slog.Warn("rabbitmq: permanent error, rejecting to DLX",
					slog.String("event_id", entry.ID),
					slog.String("topic", topic),
					slog.String("consumer_group", cb.config.ConsumerGroup),
					slog.Any("error", lastResult.Err))
				// Release idempotency key so future reprocessing is possible if
				// the message is manually republished from DLQ.
				if relErr := cb.checker.Release(context.WithoutCancel(ctx), idempotencyKey); relErr != nil {
					slog.Error("rabbitmq: failed to release idempotency key",
						slog.String("event_id", entry.ID),
						slog.String("key", idempotencyKey),
						slog.String("error", relErr.Error()))
				}
				return outbox.HandleResult{
					Disposition: outbox.DispositionReject,
					Err:         lastResult.Err,
				}
			}

			// Transient error — backoff before retry.
			if attempt < cb.config.RetryCount-1 {
				// Early exit on shutdown to avoid blocking during backoff.
				if ctx.Err() != nil {
					if relErr := cb.checker.Release(context.WithoutCancel(ctx), idempotencyKey); relErr != nil {
						slog.Error("rabbitmq: failed to release idempotency key on shutdown",
							slog.String("event_id", entry.ID),
							slog.String("key", idempotencyKey),
							slog.String("error", relErr.Error()))
					}
					return outbox.HandleResult{
						Disposition: outbox.DispositionRequeue,
						Err:         ctx.Err(),
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
					if relErr := cb.checker.Release(context.WithoutCancel(ctx), idempotencyKey); relErr != nil {
						slog.Error("rabbitmq: failed to release idempotency key on shutdown",
							slog.String("event_id", entry.ID),
							slog.String("key", idempotencyKey),
							slog.String("error", relErr.Error()))
					}
					return outbox.HandleResult{
						Disposition: outbox.DispositionRequeue,
						Err:         ctx.Err(),
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
		if relErr := cb.checker.Release(context.WithoutCancel(ctx), idempotencyKey); relErr != nil {
			slog.Error("rabbitmq: failed to release idempotency key",
				slog.String("event_id", entry.ID),
				slog.String("key", idempotencyKey),
				slog.String("error", relErr.Error()))
		}
		return outbox.HandleResult{
			Disposition: outbox.DispositionReject,
			Err:         lastResult.Err,
		}
	}
}
