//go:build integration

package rabbitmq

import (
	"testing"
)

// TestIntegration_PublishConsume publishes a message and consumes it
// from a real RabbitMQ broker, asserting payload integrity.
func TestIntegration_PublishConsume(t *testing.T) {
	t.Skip("stub: requires RabbitMQ (docker compose up)")
}

// TestIntegration_ConsumerBaseRetry verifies that ConsumerBase retries
// a transiently-failing handler up to the configured limit.
func TestIntegration_ConsumerBaseRetry(t *testing.T) {
	t.Skip("stub: requires RabbitMQ (docker compose up)")
}

// TestIntegration_DLQ publishes a message whose handler returns a
// permanent error, and asserts the message arrives in the dead-letter
// queue.
func TestIntegration_DLQ(t *testing.T) {
	t.Skip("stub: requires RabbitMQ (docker compose up)")
}

// TestIntegration_ConnectionRecovery kills the AMQP connection and
// asserts the adapter reconnects automatically.
func TestIntegration_ConnectionRecovery(t *testing.T) {
	t.Skip("stub: requires RabbitMQ (docker compose up)")
}
