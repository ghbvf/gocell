//go:build integration

// Package postgres provides the PostgreSQL adapter for GoCell.
// Integration tests require a running PostgreSQL instance.
package postgres

import "testing"

// TestIntegration_PingConnection verifies basic connectivity to PostgreSQL.
func TestIntegration_PingConnection(t *testing.T) {
	t.Skip("stub: requires running PostgreSQL instance")
}

// TestIntegration_MigrateUp verifies that schema migrations apply cleanly.
func TestIntegration_MigrateUp(t *testing.T) {
	t.Skip("stub: requires running PostgreSQL instance")
}

// TestIntegration_CRUDRoundTrip verifies insert/select/update/delete.
func TestIntegration_CRUDRoundTrip(t *testing.T) {
	t.Skip("stub: requires running PostgreSQL instance")
}

// TestIntegration_OutboxFullChain (T71) verifies the full outbox pattern:
// insert row + outbox entry in single transaction, poll, publish, mark delivered.
func TestIntegration_OutboxFullChain(t *testing.T) {
	t.Skip("stub: requires running PostgreSQL + outbox poller")
}

// TestIntegration_TransactionRollback verifies that a failed transaction
// rolls back both the business row and the outbox entry.
func TestIntegration_TransactionRollback(t *testing.T) {
	t.Skip("stub: requires running PostgreSQL instance")
}

// TestIntegration_Close verifies graceful shutdown releases the connection pool.
func TestIntegration_Close(t *testing.T) {
	t.Skip("stub: requires running PostgreSQL instance")
}
