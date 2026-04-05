// Package outbox provides the transactional outbox pattern for the GoCell
// kernel.
//
// The outbox pattern guarantees at-least-once event delivery by storing
// events in the same database transaction as business data. A background
// poller reads pending entries and publishes them to the message broker.
//
// # Flow
//
//  1. Business logic + outbox entry written in a single DB transaction
//  2. Outbox poller reads pending entries (SELECT ... FOR UPDATE SKIP LOCKED)
//  3. Events are published to the message broker (e.g. RabbitMQ)
//  4. Successfully published entries are marked as delivered
//
// # Consistency Level
//
// The outbox pattern is required for L2 (OutboxFact) and above. L0/L1
// operations do not need outbox support.
package outbox
