//go:build integration

package postgres

import (
	"testing"
)

// TestIntegration_PoolConnect verifies that Pool can connect to a real
// PostgreSQL instance and pass the Health() probe.
func TestIntegration_PoolConnect(t *testing.T) {
	t.Skip("stub: requires PostgreSQL (docker compose up)")
}

// TestIntegration_MigratorUpDown runs migrations up then down on a real
// database and verifies idempotency.
func TestIntegration_MigratorUpDown(t *testing.T) {
	t.Skip("stub: requires PostgreSQL (docker compose up)")
}

// TestIntegration_TxManagerNestedSavepoints creates nested transactions
// with savepoints and validates partial rollback semantics.
func TestIntegration_TxManagerNestedSavepoints(t *testing.T) {
	t.Skip("stub: requires PostgreSQL (docker compose up)")
}

// TestIntegration_OutboxFullChain writes an outbox message inside a
// transaction, runs the relay, and asserts the message is published and
// the outbox row is marked as relayed.
func TestIntegration_OutboxFullChain(t *testing.T) {
	t.Skip("stub: requires PostgreSQL + RabbitMQ (docker compose up)")
}
