package metadata

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helper: build a complete in-memory project ---

func fullProjectFS() fstest.MapFS {
	return fstest.MapFS{
		// --- cells ---
		"cells/access-core/cell.yaml": &fstest.MapFile{Data: []byte(`id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies: []
`)},
		"cells/audit-core/cell.yaml": &fstest.MapFile{Data: []byte(`id: audit-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_audit_core
verify:
  smoke:
    - smoke.audit-core.startup
l0Dependencies: []
`)},

		// --- slices ---
		"cells/access-core/slices/session-login/slice.yaml": &fstest.MapFile{Data: []byte(`id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
  - contract: event.session.created.v1
    role: publish
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
    - contract.event.session.created.v1.publish
  waivers: []
`)},

		// --- contracts ---
		"contracts/http/auth/login/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
schemaRefs:
  request: request.schema.json
  response: response.schema.json
`)},
		"contracts/event/session/created/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: event.session.created.v1
kind: event
ownerCell: access-core
consistencyLevel: L2
lifecycle: active
endpoints:
  publisher: access-core
  subscribers:
    - audit-core
replayable: true
idempotencyKey: event_id
deliverySemantics: at-least-once
`)},

		// --- journeys ---
		"journeys/J-sso-login.yaml": &fstest.MapFile{Data: []byte(`id: J-sso-login
goal: User completes SSO login
owner:
  team: platform
  role: journey-owner
cells:
  - access-core
  - audit-core
contracts:
  - http.auth.login.v1
  - event.session.created.v1
passCriteria:
  - text: OIDC redirect completed
    mode: auto
    checkRef: journey.J-sso-login.oidc-redirect
  - text: Security review
    mode: manual
`)},

		// --- assemblies ---
		"assemblies/core-bundle/assembly.yaml": &fstest.MapFile{Data: []byte(`id: core-bundle
cells:
  - access-core
  - audit-core
build:
  entrypoint: src/cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
`)},

		// --- status-board ---
		"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(`- journeyId: J-sso-login
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
- journeyId: J-session-refresh
  state: todo
  risk: low
  blocker: ""
  updatedAt: 2026-04-05
`)},

		// --- actors ---
		"actors.yaml": &fstest.MapFile{Data: []byte(`- id: edge-bff
  type: external
  maxConsistencyLevel: L1
`)},
	}
}

func TestParseFS_FullProject(t *testing.T) {
	p := NewParser("")
	pm, err := p.ParseFS(fullProjectFS())
	require.NoError(t, err)

	// Cells
	assert.Len(t, pm.Cells, 2)
	assert.Contains(t, pm.Cells, "access-core")
	assert.Contains(t, pm.Cells, "audit-core")
	assert.Equal(t, "core", pm.Cells["access-core"].Type)
	assert.Equal(t, "L2", pm.Cells["access-core"].ConsistencyLevel)
	assert.Equal(t, "platform", pm.Cells["access-core"].Owner.Team)
	assert.Equal(t, "cell_access_core", pm.Cells["access-core"].Schema.Primary)
	assert.Equal(t, []string{"smoke.access-core.startup"}, pm.Cells["access-core"].Verify.Smoke)

	// Slices
	assert.Len(t, pm.Slices, 1)
	assert.Contains(t, pm.Slices, "access-core/session-login")
	sl := pm.Slices["access-core/session-login"]
	assert.Equal(t, "session-login", sl.ID)
	assert.Equal(t, "access-core", sl.BelongsToCell)
	assert.Len(t, sl.ContractUsages, 2)
	assert.Equal(t, "http.auth.login.v1", sl.ContractUsages[0].Contract)
	assert.Equal(t, "serve", sl.ContractUsages[0].Role)

	// Contracts
	assert.Len(t, pm.Contracts, 2)
	assert.Contains(t, pm.Contracts, "http.auth.login.v1")
	assert.Contains(t, pm.Contracts, "event.session.created.v1")

	httpC := pm.Contracts["http.auth.login.v1"]
	assert.Equal(t, "http", httpC.Kind)
	assert.Equal(t, "access-core", httpC.Endpoints.Server)
	assert.Equal(t, []string{"edge-bff"}, httpC.Endpoints.Clients)
	assert.Equal(t, "request.schema.json", httpC.SchemaRefs.Request)
	assert.Nil(t, httpC.Replayable)

	eventC := pm.Contracts["event.session.created.v1"]
	assert.Equal(t, "event", eventC.Kind)
	assert.Equal(t, "access-core", eventC.Endpoints.Publisher)
	assert.Equal(t, []string{"audit-core"}, eventC.Endpoints.Subscribers)
	require.NotNil(t, eventC.Replayable)
	assert.True(t, *eventC.Replayable)
	assert.Equal(t, "event_id", eventC.IdempotencyKey)
	assert.Equal(t, "at-least-once", eventC.DeliverySemantics)

	// Journeys
	assert.Len(t, pm.Journeys, 1)
	assert.Contains(t, pm.Journeys, "J-sso-login")
	j := pm.Journeys["J-sso-login"]
	assert.Equal(t, "User completes SSO login", j.Goal)
	assert.Equal(t, []string{"access-core", "audit-core"}, j.Cells)
	assert.Len(t, j.PassCriteria, 2)
	assert.Equal(t, "auto", j.PassCriteria[0].Mode)
	assert.Equal(t, "manual", j.PassCriteria[1].Mode)
	assert.Equal(t, "", j.PassCriteria[1].CheckRef)

	// Assemblies
	assert.Len(t, pm.Assemblies, 1)
	assert.Contains(t, pm.Assemblies, "core-bundle")
	a := pm.Assemblies["core-bundle"]
	assert.Equal(t, []string{"access-core", "audit-core"}, a.Cells)
	assert.Equal(t, "k8s", a.Build.DeployTemplate)

	// Status Board
	assert.Len(t, pm.StatusBoard, 2)
	assert.Equal(t, "J-sso-login", pm.StatusBoard[0].JourneyID)
	assert.Equal(t, "doing", pm.StatusBoard[0].State)

	// Actors
	assert.Len(t, pm.Actors, 1)
	assert.Equal(t, "edge-bff", pm.Actors[0].ID)
	assert.Equal(t, "external", pm.Actors[0].Type)
	assert.Equal(t, "L1", pm.Actors[0].MaxConsistencyLevel)
}

func TestParseFS_EmptyProject(t *testing.T) {
	p := NewParser("")
	pm, err := p.ParseFS(fstest.MapFS{})
	require.NoError(t, err)

	assert.Empty(t, pm.Cells)
	assert.Empty(t, pm.Slices)
	assert.Empty(t, pm.Contracts)
	assert.Empty(t, pm.Journeys)
	assert.Empty(t, pm.Assemblies)
	assert.Empty(t, pm.StatusBoard)
	assert.Empty(t, pm.Actors)
}

func TestParseFS_MissingActorsNoError(t *testing.T) {
	fs := fstest.MapFS{
		"cells/my-cell/cell.yaml": &fstest.MapFile{Data: []byte(`id: my-cell
type: core
consistencyLevel: L1
owner:
  team: test
  role: cell-owner
schema:
  primary: cell_my_cell
verify:
  smoke:
    - smoke.my-cell.startup
`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	assert.Len(t, pm.Cells, 1)
	assert.Empty(t, pm.Actors)
}

func TestParseFS_InvalidYAML(t *testing.T) {
	fs := fstest.MapFS{
		"cells/bad-cell/cell.yaml": &fstest.MapFile{Data: []byte(`{{{ not valid yaml`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse cells/bad-cell/cell.yaml")
	assert.Contains(t, err.Error(), "ERR_METADATA_INVALID")
}

func TestParseFS_InvalidSliceYAML(t *testing.T) {
	fs := fstest.MapFS{
		"cells/my-cell/slices/bad-slice/slice.yaml": &fstest.MapFile{Data: []byte(`:::broken`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse cells/my-cell/slices/bad-slice/slice.yaml")
}

func TestParseFS_InvalidContractYAML(t *testing.T) {
	fs := fstest.MapFS{
		"contracts/http/auth/login/v1/contract.yaml": &fstest.MapFile{Data: []byte(`[[[broken`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse contracts/http/auth/login/v1/contract.yaml")
}

func TestParseFS_InvalidJourneyYAML(t *testing.T) {
	fs := fstest.MapFS{
		"journeys/J-broken.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse journeys/J-broken.yaml")
}

func TestParseFS_InvalidAssemblyYAML(t *testing.T) {
	fs := fstest.MapFS{
		"assemblies/bad/assembly.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse assemblies/bad/assembly.yaml")
}

func TestParseFS_InvalidStatusBoardYAML(t *testing.T) {
	fs := fstest.MapFS{
		"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse journeys/status-board.yaml")
}

func TestParseFS_InvalidActorsYAML(t *testing.T) {
	fs := fstest.MapFS{
		"actors.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse actors.yaml")
}

func TestParseFS_DeepContractPath(t *testing.T) {
	fs := fstest.MapFS{
		"contracts/event/session/created/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: event.session.created.v1
kind: event
ownerCell: access-core
consistencyLevel: L2
lifecycle: active
endpoints:
  publisher: access-core
  subscribers:
    - audit-core
replayable: true
idempotencyKey: event_id
deliverySemantics: at-least-once
`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	assert.Len(t, pm.Contracts, 1)
	assert.Contains(t, pm.Contracts, "event.session.created.v1")
	c := pm.Contracts["event.session.created.v1"]
	assert.Equal(t, "event", c.Kind)
	assert.Equal(t, "access-core", c.Endpoints.Publisher)
}

func TestParseFS_NonMetadataFilesIgnored(t *testing.T) {
	fs := fstest.MapFS{
		"cells/access-core/cell.yaml": &fstest.MapFile{Data: []byte(`id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
`)},
		// These should be ignored:
		"README.md":                             &fstest.MapFile{Data: []byte(`# readme`)},
		"cells/access-core/main.go":             &fstest.MapFile{Data: []byte(`package main`)},
		"journeys/status-board.yaml.bak":        &fstest.MapFile{Data: []byte(`backup`)},
		"contracts/http/auth/login/v1/schema.json": &fstest.MapFile{Data: []byte(`{}`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	assert.Len(t, pm.Cells, 1)
	assert.Empty(t, pm.Slices)
	assert.Empty(t, pm.Contracts)
	assert.Empty(t, pm.Journeys)
	assert.Empty(t, pm.Assemblies)
}

func TestParseFS_JourneyPatternFiltering(t *testing.T) {
	fs := fstest.MapFS{
		// Valid journey
		"journeys/J-sso-login.yaml": &fstest.MapFile{Data: []byte(`id: J-sso-login
goal: SSO login
owner:
  team: platform
  role: journey-owner
cells: []
contracts: []
passCriteria: []
`)},
		// status-board should not be parsed as journey
		"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(`- journeyId: J-sso-login
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
`)},
		// Non-J- yaml should be ignored
		"journeys/notes.yaml": &fstest.MapFile{Data: []byte(`foo: bar`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	assert.Len(t, pm.Journeys, 1)
	assert.Contains(t, pm.Journeys, "J-sso-login")
	assert.Len(t, pm.StatusBoard, 1)
}

func TestParseFS_MultipleSlicesSameCell(t *testing.T) {
	fs := fstest.MapFS{
		"cells/access-core/slices/session-login/slice.yaml": &fstest.MapFile{Data: []byte(`id: session-login
belongsToCell: access-core
contractUsages: []
verify:
  unit: []
  contract: []
`)},
		"cells/access-core/slices/rbac-check/slice.yaml": &fstest.MapFile{Data: []byte(`id: rbac-check
belongsToCell: access-core
contractUsages: []
verify:
  unit: []
  contract: []
`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	assert.Len(t, pm.Slices, 2)
	assert.Contains(t, pm.Slices, "access-core/session-login")
	assert.Contains(t, pm.Slices, "access-core/rbac-check")
}

func TestParseFS_SliceWithWaivers(t *testing.T) {
	fs := fstest.MapFS{
		"cells/access-core/slices/session-login/slice.yaml": &fstest.MapFile{Data: []byte(`id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
  - contract: http.config.get.v1
    role: call
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
  waivers:
    - contract: http.config.get.v1
      owner: platform-team
      reason: read-only config call
      expiresAt: 2026-06-01
`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	sl := pm.Slices["access-core/session-login"]
	require.NotNil(t, sl)
	require.Len(t, sl.Verify.Waivers, 1)
	assert.Equal(t, "http.config.get.v1", sl.Verify.Waivers[0].Contract)
	assert.Equal(t, "platform-team", sl.Verify.Waivers[0].Owner)
	assert.Equal(t, "2026-06-01", sl.Verify.Waivers[0].ExpiresAt)
}

func TestParseFS_SliceOmitsBelongsToCell(t *testing.T) {
	fs := fstest.MapFS{
		"cells/access-core/slices/session-login/slice.yaml": &fstest.MapFile{Data: []byte(`id: session-login
contractUsages: []
verify:
  unit: []
  contract: []
`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	// Map key should be cellID/sliceID, not "/session-login"
	assert.Len(t, pm.Slices, 1)
	assert.Contains(t, pm.Slices, "access-core/session-login")
	assert.NotContains(t, pm.Slices, "/session-login")

	// BelongsToCell should be backfilled from path
	sl := pm.Slices["access-core/session-login"]
	require.NotNil(t, sl)
	assert.Equal(t, "access-core", sl.BelongsToCell)
}

func TestParseFS_DuplicateCellID(t *testing.T) {
	fs := fstest.MapFS{
		"cells/access-core/cell.yaml": &fstest.MapFile{Data: []byte(`id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
`)},
		"cells/access-core-v2/cell.yaml": &fstest.MapFile{Data: []byte(`id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "access-core")
}

func TestParseFS_DuplicateContractID(t *testing.T) {
	fs := fstest.MapFS{
		"contracts/http/auth/login/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients: []
`)},
		"contracts/http/auth/login/v2/contract.yaml": &fstest.MapFile{Data: []byte(`id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients: []
`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "http.auth.login.v1")
}

func TestParseFS_DuplicateJourneyID(t *testing.T) {
	// fstest.MapFS does not allow two files with the same path, so we simulate
	// duplicate journey IDs by placing them in different directories.
	// However, matchJourneyYAML only matches "journeys/J-*.yaml" (exactly 2 segments).
	// Instead we use two journey files with different names but the same id field.
	fs := fstest.MapFS{
		"journeys/J-sso-login.yaml": &fstest.MapFile{Data: []byte(`id: J-sso-login
goal: SSO login
owner:
  team: platform
  role: journey-owner
cells: []
contracts: []
passCriteria: []
`)},
		"journeys/J-sso-login-copy.yaml": &fstest.MapFile{Data: []byte(`id: J-sso-login
goal: SSO login copy
owner:
  team: platform
  role: journey-owner
cells: []
contracts: []
passCriteria: []
`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "J-sso-login")
}

func TestParseFS_DuplicateAssemblyID(t *testing.T) {
	fs := fstest.MapFS{
		"assemblies/core-bundle/assembly.yaml": &fstest.MapFile{Data: []byte(`id: core-bundle
cells:
  - access-core
build:
  entrypoint: src/cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
`)},
		"assemblies/core-bundle-v2/assembly.yaml": &fstest.MapFile{Data: []byte(`id: core-bundle
cells:
  - access-core
build:
  entrypoint: src/cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "core-bundle")
}

func TestParseFS_DuplicateSliceID(t *testing.T) {
	// Two slice files in different cell directories but producing the same composite key
	// is unlikely since key = cellID/sliceID. Instead, we can't have two files at the
	// same path in fstest.MapFS. So we create a scenario where the same cell directory
	// has a duplicate slice id — but that requires the same path which isn't possible.
	// The realistic scenario: two different cell dirs contain slices that map to the
	// same key (cellID/sliceID), which can't happen since cellID comes from the path.
	// Instead, test that two slices in the same cell with the same id: field fail.
	// This requires two different slice directories under the same cell, both declaring
	// the same id in YAML.
	fs := fstest.MapFS{
		"cells/access-core/slices/session-login/slice.yaml": &fstest.MapFile{Data: []byte(`id: dup-slice
belongsToCell: access-core
contractUsages: []
verify:
  unit: []
  contract: []
`)},
		"cells/access-core/slices/session-logout/slice.yaml": &fstest.MapFile{Data: []byte(`id: dup-slice
belongsToCell: access-core
contractUsages: []
verify:
  unit: []
  contract: []
`)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "dup-slice")
}

func TestParseFS_CellWithL0Dependencies(t *testing.T) {
	fs := fstest.MapFS{
		"cells/access-core/cell.yaml": &fstest.MapFile{Data: []byte(`id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies:
  - cell: shared-crypto
    reason: deterministic hashing
`)},
	}

	p := NewParser("")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	cell := pm.Cells["access-core"]
	require.NotNil(t, cell)
	require.Len(t, cell.L0Dependencies, 1)
	assert.Equal(t, "shared-crypto", cell.L0Dependencies[0].Cell)
	assert.Equal(t, "deterministic hashing", cell.L0Dependencies[0].Reason)
}
