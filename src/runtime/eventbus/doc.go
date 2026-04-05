// Package eventbus provides an in-memory implementation of kernel/outbox.Publisher
// and kernel/outbox.Subscriber for development and testing.
//
// InMemoryEventBus delivers messages via buffered channels with at-most-once
// semantics: messages are lost on process restart. It includes built-in retry
// logic (3 attempts with exponential backoff: 100ms, 200ms, 400ms) and a dead
// letter slice for messages that exhaust all retries.
//
// For production deployments replace this bus with adapters/rabbitmq, which
// provides durable at-least-once delivery and proper dead-letter queues.
//
// ref: ThreeDotsLabs/watermill message/message.go — Message model, Ack/Nack pattern
// Adopted: topic-based pub/sub, callback handler pattern.
// Deviated: channel-based in-memory delivery; no persistence.
//
// # Usage
//
//	bus := eventbus.New(eventbus.WithBufferSize(512))
//
//	// Publish
//	err := bus.Publish(ctx, "session.events", payload)
//
//	// Subscribe (blocks until ctx cancelled or bus closed)
//	err := bus.Subscribe(ctx, "session.events", func(ctx context.Context, entry outbox.Entry) error {
//	    // process event ...
//	    return nil // nil → ACK; non-nil → NACK + retry
//	})
//
//	// Inspect dead letters in tests
//	dl := bus.DrainDeadLetters()
//
// # Limitations
//
//   - at-most-once delivery (messages lost on restart)
//   - per-IP rate limiting is not shared across processes
//   - no message persistence or replay capability
package eventbus
