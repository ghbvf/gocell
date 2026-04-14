//go:build integration

package metadata

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testProjectRoot returns the absolute path to the src/ directory.
func testProjectRoot(t *testing.T) string {
	t.Helper()
	// This file lives at src/kernel/metadata/parser_integration_test.go.
	// Walk up three levels to reach src/.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func TestParseRealProject(t *testing.T) {
	root := testProjectRoot(t)
	p := NewParser(root)
	pm, err := p.Parse()
	require.NoError(t, err, "Parse should succeed on real project files")

	// --- Cells: at least the 3 original cells (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Cells), 3, "expected at least 3 cells")
	assert.LessOrEqual(t, len(pm.Cells), 6, "unexpected extra cells parsed — update this bound if new cells were added intentionally")
	for _, id := range []string{"access-core", "audit-core", "config-core"} {
		assert.Contains(t, pm.Cells, id, "missing cell %s", id)
	}

	// --- Slices: at least the 16 original slices (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Slices), 16, "expected at least 16 slices")
	assert.LessOrEqual(t, len(pm.Slices), 24, "unexpected extra slices parsed — update this bound if new slices were added intentionally")
	expectedSlices := []string{
		"access-core/session-login",
		"access-core/session-validate",
		"access-core/rbac-check",
		"access-core/identity-manage",
		"access-core/session-refresh",
		"access-core/session-logout",
		"access-core/authorization-decide",
		"audit-core/audit-append",
		"audit-core/audit-query",
		"audit-core/audit-verify",
		"audit-core/audit-archive",
		"config-core/config-read",
		"config-core/config-write",
		"config-core/config-publish",
		"config-core/config-subscribe",
		"config-core/feature-flag",
	}
	for _, key := range expectedSlices {
		assert.Contains(t, pm.Slices, key, "missing slice %s", key)
	}

	// --- Contracts: at least the 26 contracts after per-operation split (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Contracts), 26, "expected at least 26 contracts after per-operation split")
	assert.LessOrEqual(t, len(pm.Contracts), 35, "unexpected extra contracts parsed — update this bound if new contracts were added intentionally")
	expectedContracts := []string{
		"http.auth.login.v1",
		"http.auth.refresh.v1",
		"http.config.get.v1",
		"http.auth.user.create.v1",
		"http.auth.user.get.v1",
		"http.config.flags.list.v1",
		"http.config.flags.get.v1",
		"http.config.flags.evaluate.v1",
		"http.device.register.v1",
		"http.device.status.v1",
		"http.order.create.v1",
		"http.order.get.v1",
		"http.order.list.v1",
		"command.device-command.enqueue.v1",
		"command.device-command.list.v1",
		"command.device-command.ack.v1",
		"event.session.created.v1",
		"event.audit.appended.v1",
		"event.config.changed.v1",
		"event.session.revoked.v1",
		"event.user.created.v1",
		"event.user.locked.v1",
		"event.audit.integrity-verified.v1",
		"event.config.rollback.v1",
	}
	for _, id := range expectedContracts {
		assert.Contains(t, pm.Contracts, id, "missing contract %s", id)
	}

	// --- Journeys: at least the 8 original journeys (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Journeys), 8, "expected at least 8 journeys")
	assert.LessOrEqual(t, len(pm.Journeys), 12, "unexpected extra journeys parsed — update this bound if new journeys were added intentionally")
	expectedJourneys := []string{
		"J-sso-login",
		"J-session-refresh",
		"J-session-logout",
		"J-user-onboarding",
		"J-account-lockout",
		"J-audit-login-trail",
		"J-config-hot-reload",
		"J-config-rollback",
	}
	for _, id := range expectedJourneys {
		assert.Contains(t, pm.Journeys, id, "missing journey %s", id)
	}

	// --- Assemblies: at least the 1 original assembly (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Assemblies), 1, "expected at least 1 assembly")
	assert.LessOrEqual(t, len(pm.Assemblies), 3, "unexpected extra assemblies parsed — update this bound if new assemblies were added intentionally")
	assert.Contains(t, pm.Assemblies, "core-bundle")

	// --- Status Board: at least the 8 original entries (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.StatusBoard), 8, "expected at least 8 status-board entries")
	assert.LessOrEqual(t, len(pm.StatusBoard), 12, "unexpected extra status-board entries parsed — update this bound if new entries were added intentionally")

	// --- Actors: 1 ---
	assert.Len(t, pm.Actors, 1, "expected 1 actor")
	assert.Equal(t, "edge-bff", pm.Actors[0].ID)

	// Spot-check a well-known cell.
	ac := pm.Cells["access-core"]
	require.NotNil(t, ac)
	assert.Equal(t, "core", ac.Type)
	assert.Equal(t, "L2", ac.ConsistencyLevel)
	assert.Equal(t, "platform", ac.Owner.Team)

	// Spot-check a contract.
	login := pm.Contracts["http.auth.login.v1"]
	require.NotNil(t, login)
	assert.Equal(t, "http", login.Kind)
	assert.Equal(t, "active", login.Lifecycle)
	assert.Equal(t, "access-core", login.Endpoints.Server)

	// Spot-check an event contract.
	evt := pm.Contracts["event.session.created.v1"]
	require.NotNil(t, evt)
	assert.Equal(t, "event", evt.Kind)
	require.NotNil(t, evt.Replayable)
	assert.True(t, *evt.Replayable)
	assert.Equal(t, "event_id", evt.IdempotencyKey)
}
