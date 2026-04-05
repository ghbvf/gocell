// Package outbox defines interfaces for the transactional outbox pattern:
// Writer (insert within a transaction), Relay (poll-and-publish), Publisher
// (fire-and-forget), and Subscriber (consume). Implementations live in
// adapters/ (e.g., adapters/postgres, adapters/rabbitmq).
package outbox
