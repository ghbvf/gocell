//go:build integration

// Package rabbitmq provides the RabbitMQ adapter for GoCell.
// Integration tests require a running RabbitMQ instance.
package rabbitmq

import "testing"

// TestIntegration_ConnectAndClose verifies basic AMQP connectivity.
func TestIntegration_ConnectAndClose(t *testing.T) {
	t.Skip("stub: requires running RabbitMQ instance")
}

// TestIntegration_PublishConsume verifies a basic publish-then-consume round trip.
func TestIntegration_PublishConsume(t *testing.T) {
	t.Skip("stub: requires running RabbitMQ instance")
}

// TestIntegration_DLQ (T72) verifies that messages that fail processing
// are routed to the dead-letter queue after retry exhaustion.
func TestIntegration_DLQ(t *testing.T) {
	t.Skip("stub: requires running RabbitMQ instance with DLQ configured")
}

// TestIntegration_NACKRetry verifies that NACK triggers re-delivery with backoff.
func TestIntegration_NACKRetry(t *testing.T) {
	t.Skip("stub: requires running RabbitMQ instance")
}

// TestIntegration_Close verifies graceful shutdown drains in-flight messages.
func TestIntegration_Close(t *testing.T) {
	t.Skip("stub: requires running RabbitMQ instance")
}
