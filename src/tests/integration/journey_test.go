//go:build integration

package integration

import (
	"testing"
)

// TestJourney_AuditLoginTrail boots a full assembly, performs a login via
// access-core, and verifies that audit-core recorded the expected
// audit trail entries (J-audit-login-trail).
func TestJourney_AuditLoginTrail(t *testing.T) {
	t.Skip("stub: requires full assembly (docker compose up)")
}

// TestJourney_ConfigHotReload boots a full assembly, updates a
// config-core entry, and verifies that subscribed cells receive the
// change notification within the configured poll interval
// (J-config-hot-reload).
func TestJourney_ConfigHotReload(t *testing.T) {
	t.Skip("stub: requires full assembly (docker compose up)")
}

// TestJourney_ConfigRollback boots a full assembly, publishes a config
// change, then rolls it back, and verifies the previous version is
// restored (J-config-rollback).
func TestJourney_ConfigRollback(t *testing.T) {
	t.Skip("stub: requires full assembly (docker compose up)")
}
