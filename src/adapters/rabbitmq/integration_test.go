//go:build integration

// Package rabbitmq_test contains integration tests for the RabbitMQ adapter.
// These tests require a running RabbitMQ instance (via Docker/testcontainers).
package rabbitmq_test

import "testing"

// TestIntegration_RabbitMQConnection verifies basic connection and channel
// setup against a real RabbitMQ instance.
func TestIntegration_RabbitMQConnection(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup
	// 1. Start rabbitmq container
	// 2. Establish AMQP connection
	// 3. Open channel
	// 4. Verify health check
}

// TestIntegration_RabbitMQPublishConsume verifies basic publish/subscribe
// through exchange and queue bindings.
func TestIntegration_RabbitMQPublishConsume(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup
	// 1. Declare exchange and queue
	// 2. Bind queue to exchange
	// 3. Publish message
	// 4. Consume and verify message content
}

// TestIntegration_RabbitMQConsumerGroup verifies consumer group semantics
// with multiple consumers on the same queue.
func TestIntegration_RabbitMQConsumerGroup(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: testcontainers setup
	// 1. Start 2 consumers on same queue
	// 2. Publish N messages
	// 3. Verify messages distributed across consumers
	// 4. Verify no message processed twice
}

// TestIntegration_RabbitMQDLQ verifies dead-letter queue routing when
// a consumer permanently fails to process a message.
//
// Consumer: cg-test-dlq
// Idempotency key: test:dlq:{event-id}, TTL 24h
// ACK timing: after business logic + idempotency key written
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
func TestIntegration_RabbitMQDLQ(t *testing.T) {
	t.Skip("requires Docker: DLQ routing on permanent consumer failure")
	// TODO: testcontainers setup
	// 1. Declare primary queue with DLX (dead-letter exchange)
	// 2. Declare dead-letter queue bound to DLX
	// 3. Publish message that will cause permanent failure (unmarshal error)
	// 4. Consumer NACKs without requeue (permanent error)
	// 5. Verify message appears in dead-letter queue
	// 6. Verify dead-letter message is observable (headers, count)
}

// TestIntegration_RabbitMQDLQRetryExhaustion verifies that messages are
// routed to DLQ after retry budget is exhausted.
func TestIntegration_RabbitMQDLQRetryExhaustion(t *testing.T) {
	t.Skip("requires Docker: DLQ after retry budget exhausted")
	// TODO: testcontainers setup
	// 1. Configure consumer with max 3 retries
	// 2. Publish message that causes transient failure every time
	// 3. Verify message is retried 3 times with backoff
	// 4. After 3 retries, verify message lands in DLQ
}

// TestIntegration_RabbitMQNACKBackoff verifies NACK with backoff retry
// for transient errors.
func TestIntegration_RabbitMQNACKBackoff(t *testing.T) {
	t.Skip("requires Docker: NACK + exponential backoff on transient error")
	// TODO: testcontainers setup
	// 1. Publish message
	// 2. Consumer returns transient error on first attempt
	// 3. Verify NACK triggers redelivery
	// 4. Consumer succeeds on second attempt
	// 5. Verify ACK
}

// TestIntegration_RabbitMQIdempotency verifies that ConsumerBase prevents
// duplicate message processing using idempotency keys.
func TestIntegration_RabbitMQIdempotency(t *testing.T) {
	t.Skip("requires Docker: idempotency check prevents duplicate processing")
	// TODO: testcontainers setup (rabbitmq + redis for idempotency store)
	// 1. Publish message with event_id
	// 2. Consumer processes and stores idempotency key
	// 3. Redeliver same message (simulate redelivery)
	// 4. Verify second processing is skipped (idempotency hit)
}
