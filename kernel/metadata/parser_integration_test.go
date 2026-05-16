//go:build integration

package metadata

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testProjectRoot returns the absolute path to the project root directory.
func testProjectRoot(t *testing.T) string {
	t.Helper()
	// This file lives at kernel/metadata/parser_integration_test.go.
	// Walk up two levels to reach the project root.
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
	for _, id := range []string{"accesscore", "auditcore", "configcore"} {
		assert.Contains(t, pm.Cells, id, "missing cell %s", id)
	}

	// --- Slices: at least the 18 slices after auditverify removal (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Slices), 18, "expected at least 18 slices")
	assert.LessOrEqual(t, len(pm.Slices), 30, "unexpected extra slices parsed — update this bound if new slices were added intentionally")
	expectedSlices := []string{
		"accesscore/sessionlogin",
		"accesscore/sessionvalidate",
		"accesscore/rbaccheck",
		"accesscore/identitymanage",
		"accesscore/sessionrefresh",
		"accesscore/sessionlogout",
		"accesscore/authorizationdecide",
		"accesscore/rbacassign",
		"accesscore/configreceive",
		"auditcore/auditappendsession",
		"auditcore/auditappenduser",
		"auditcore/auditappendconfig",
		"auditcore/auditappendrole",
		"auditcore/auditquery",
		"configcore/configread",
		"configcore/configwrite",
		"configcore/configpublish",
		"configcore/configsubscribe",
		"configcore/featureflag",
		"configcore/flagwrite",
	}
	for _, key := range expectedSlices {
		assert.Contains(t, pm.Slices, key, "missing slice %s", key)
	}

	// --- Contracts: at least the 26 contracts after integrity-verified removal (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Contracts), 26, "expected at least 26 contracts after integrity-verified removal")
	assert.LessOrEqual(t, len(pm.Contracts), 80, "unexpected extra contracts parsed — update this bound if new contracts were added intentionally")
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
		"command.device-command.dequeue.v1",
		"command.device-command.ack.v1",
		"event.session.created.v1",
		"event.audit.appended.v1",
		"event.config.entry-upserted.v1",
		"event.config.entry-deleted.v1",
		"event.config.version-published.v1",
		"event.session.revoked.v1",
		"event.user.created.v1",
		"event.user.locked.v1",
		"event.config.rollback.v1",
	}
	for _, id := range expectedContracts {
		assert.Contains(t, pm.Contracts, id, "missing contract %s", id)
	}

	// --- Journeys: at least the 8 original journeys (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Journeys), 8, "expected at least 8 journeys")
	assert.LessOrEqual(t, len(pm.Journeys), 12, "unexpected extra journeys parsed — update this bound if new journeys were added intentionally")
	expectedJourneys := []string{
		"J-ssologin",
		"J-sessionrefresh",
		"J-sessionlogout",
		"J-useronboarding",
		"J-accountlockout",
		"J-auditlogintrail",
		"J-confighotreload",
		"J-configrollback",
	}
	for _, id := range expectedJourneys {
		assert.Contains(t, pm.Journeys, id, "missing journey %s", id)
	}
	assert.Contains(t, pm.Journeys, "J-ordercreate", "example journey J-ordercreate must be parsed")

	// --- Assemblies: at least the 1 original assembly (upper bound catches over-parse) ---
	assert.GreaterOrEqual(t, len(pm.Assemblies), 1, "expected at least 1 assembly")
	assert.LessOrEqual(t, len(pm.Assemblies), 3, "unexpected extra assemblies parsed — update this bound if new assemblies were added intentionally")
	assert.Contains(t, pm.Assemblies, "corebundle")

	// --- Status Board: at least the 8 original entries (upper bound catches over-parse) ---
	//
	// J-ordercreate (an example journey under examples/todoorder/journeys/)
	// is NOT tracked in the platform journeys/status-board.yaml — example
	// projects manage their own readiness. This is enforced by validateADV01's
	// examples/ exemption and the parallel CHECK-JOURNEY-NO-STATUS-ENTRY
	// exemption in cmd/gocell/app/check.go::journeyStatusCheck.
	assert.GreaterOrEqual(t, len(pm.StatusBoard), 8, "expected at least 8 platform status-board entries")
	assert.LessOrEqual(t, len(pm.StatusBoard), 11, "unexpected extra status-board entries parsed — update this bound if new platform entries were added intentionally")
	for i := range pm.StatusBoard {
		assert.NotEqual(t, "J-ordercreate", pm.StatusBoard[i].JourneyID,
			"J-ordercreate is an example journey and must not appear in the platform status-board")
	}

	// --- Actors: 4 (edge-bff + 3 external subscribers added by PR-CFG-B for ADV-05) ---
	assert.Len(t, pm.Actors, 4, "expected 4 actors")
	actorIDs := make([]string, 0, len(pm.Actors))
	for _, a := range pm.Actors {
		actorIDs = append(actorIDs, a.ID)
	}
	assert.ElementsMatch(t, []string{"edge-bff", "external-audit-sink", "example-iot-platform", "example-order-platform"}, actorIDs)

	// Spot-check a well-known cell.
	ac := pm.Cells["accesscore"]
	require.NotNil(t, ac)
	assert.Equal(t, "core", ac.Type)
	assert.Equal(t, "L3", ac.ConsistencyLevel)
	assert.Equal(t, "platform", ac.Owner.Team)

	// Spot-check a contract.
	login := pm.Contracts["http.auth.login.v1"]
	require.NotNil(t, login)
	assert.Equal(t, "http", login.Kind)
	assert.Equal(t, "active", login.Lifecycle)
	assert.Equal(t, "accesscore", login.Endpoints.Server)

	// Spot-check an event contract.
	evt := pm.Contracts["event.session.created.v1"]
	require.NotNil(t, evt)
	assert.Equal(t, "event", evt.Kind)
	require.NotNil(t, evt.Replayable)
	assert.True(t, *evt.Replayable)
	assert.Equal(t, "event_id", evt.IdempotencyKey)
}

func TestConfigEventSubscribersMatchSliceUsages(t *testing.T) {
	root := testProjectRoot(t)
	p := NewParser(root)
	pm, err := p.Parse()
	require.NoError(t, err, "Parse should succeed on real project files")

	subscribingCells := func(contractID string) []string {
		var cells []string
		for _, s := range pm.Slices {
			for _, usage := range s.ContractUsages {
				if usage.Contract == contractID && usage.Role == "subscribe" {
					cells = append(cells, s.BelongsToCell)
				}
			}
		}
		return cells
	}

	expected := map[string][]string{
		"event.config.entry-upserted.v1":    {"accesscore", "auditcore", "configcore"},
		"event.config.entry-deleted.v1":     {"accesscore", "auditcore", "configcore"},
		"event.config.version-published.v1": {"auditcore"},
		"event.config.rollback.v1":          {"auditcore"},
	}
	for contractID, wantSubscribers := range expected {
		c := pm.Contracts[contractID]
		require.NotNil(t, c, "missing contract %s", contractID)
		assert.ElementsMatch(t, wantSubscribers, c.Endpoints.Subscribers,
			"contract %s subscribers drifted", contractID)
		assert.ElementsMatch(t, wantSubscribers, subscribingCells(contractID),
			"slice contractUsages for %s drifted from contract subscribers", contractID)
	}
}
