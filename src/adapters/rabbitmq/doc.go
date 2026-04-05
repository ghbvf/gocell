// Package rabbitmq provides a RabbitMQ adapter for the GoCell framework.
//
// It implements the event bus interfaces defined in kernel/ and runtime/,
// providing connection management, publisher confirms, consumer groups,
// dead-letter queues (DLQ), and automatic retry with backoff.
//
// # Configuration
//
//	URL:             "amqp://guest:guest@localhost:5672/"
//	PrefetchCount:   10
//	ReconnectDelay:  5s
//	MaxRetries:      3
//
// # Dead-Letter Queues
//
// Every L2+ consumer queue is automatically paired with a DLQ. Messages
// that fail after MaxRetries are routed to the DLQ for manual inspection.
// DLQ depth is exposed as an observable metric.
//
// # Consumer Pattern
//
// All consumers use ConsumerBase which provides built-in idempotency checks,
// DLQ routing, and automatic retry. See the eventbus rules in .claude/rules/.
//
// # Close
//
// Always call Close to drain in-flight messages and close the AMQP connection.
package rabbitmq
