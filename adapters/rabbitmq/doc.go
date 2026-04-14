// Package rabbitmq provides a RabbitMQ adapter for the GoCell event bus.
//
// It implements outbox.Publisher and outbox.Subscriber using amqp091-go,
// with auto-reconnect (exponential backoff), channel pooling, publisher
// confirm mode, and consumer-side ConsumerBase (idempotency + retry + DLQ).
//
// ref: Watermill watermill-amqp subscriber.go — reconnect + ACK/NACK pattern
package rabbitmq
