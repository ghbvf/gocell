// Package rabbitmq provides a RabbitMQ adapter for GoCell.
//
// This adapter implements the kernel/outbox.Publisher and kernel/outbox.Subscriber
// interfaces using AMQP 0-9-1. It replaces the Phase 2 in-memory EventBus
// (runtime/eventbus) for production deployments requiring durable, persistent
// message delivery with at-least-once semantics.
//
// The adapter integrates with the Cell outbox pattern: publishers write to the
// outbox in the same DB transaction (via outbox.Writer), and the adapter relays
// committed entries to RabbitMQ via a polling relay worker.
//
// Configuration is done via RabbitMQConfig, which can be populated from
// environment variables using ConfigFromEnv().
//
// # Usage
//
//	cfg := rabbitmq.ConfigFromEnv()
//	conn, err := rabbitmq.New(ctx, cfg)
//	if err != nil { ... }
//	defer conn.Close()
//
//	// Publish to a topic exchange
//	err = conn.Publish(ctx, "session.events", payload)
//
//	// Subscribe with automatic retry and dead-letter routing
//	err = conn.Subscribe(ctx, "session.events", handler)
//
// # Consumer Declaration
//
// Each consumer must declare its group, idempotency key strategy, and retry
// policy in source code comments. See .claude/rules/gocell/eventbus.md for
// the required declaration format.
//
// # Environment Variables
//
// See docs/guides/adapter-config-reference.md for the full variable listing.
// Key variables: RABBITMQ_URL, RABBITMQ_VHOST, RABBITMQ_EXCHANGE,
// RABBITMQ_PREFETCH_COUNT, RABBITMQ_RECONNECT_DELAY.
//
// # Error Codes
//
// All errors use the ERR_ADAPTER_RABBITMQ_* code family from pkg/errcode.
package rabbitmq
