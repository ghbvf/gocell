// Package rabbitmq provides a RabbitMQ adapter for the GoCell event bus.
//
// It implements outbox.Publisher and outbox.Subscriber using amqp091-go,
// with auto-reconnect (exponential backoff), subscriber channel pooling,
// publisher confirm mode (ephemeral channel per publish), and consumer-side
// ConsumerBase (idempotency + retry + DLQ).
//
// Publisher uses a fresh channel per Publish call (open, confirm, publish,
// close) to avoid confirm-mode channels polluting the shared pool. For
// high-throughput scenarios, a dedicated long-lived confirm channel is a
// future optimization (see Watermill pooledChannelProvider).
//
// ref: Watermill watermill-amqp — reconnect + ACK/NACK + channel lifecycle
package rabbitmq
