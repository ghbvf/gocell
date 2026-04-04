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

	// --- Cells: 3 ---
	assert.Len(t, pm.Cells, 3, "expected 3 cells")
	for _, id := range []string{"access-core", "audit-core", "config-core"} {
		assert.Contains(t, pm.Cells, id, "missing cell %s", id)
	}

	// --- Slices: 16 ---
	assert.Len(t, pm.Slices, 16, "expected 16 slices")
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

	// --- Contracts: 12 ---
	assert.Len(t, pm.Contracts, 12, "expected 12 contracts")
	expectedContracts := []string{
		"http.auth.login.v1",
		"http.config.get.v1",
		"http.auth.me.v1",
		"http.config.flags.v1",
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

	// --- Journeys: 8 ---
	assert.Len(t, pm.Journeys, 8, "expected 8 journeys")
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

	// --- Assemblies: 1 ---
	assert.Len(t, pm.Assemblies, 1, "expected 1 assembly")
	assert.Contains(t, pm.Assemblies, "core-bundle")

	// --- Status Board: 8 entries ---
	assert.Len(t, pm.StatusBoard, 8, "expected 8 status-board entries")

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
