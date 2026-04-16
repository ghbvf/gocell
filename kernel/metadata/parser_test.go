package metadata

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/pkg/errcode"
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
  entrypoint: cmd/core-bundle/main.go
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

// TestParseFS_InvalidYAMLParsing consolidates malformed-YAML coverage across
// every metadata file category into a single table. Each case produces
// ERR_METADATA_INVALID with a path prefix identifying the offending file.
func TestParseFS_InvalidYAMLParsing(t *testing.T) {
	tests := []struct {
		name    string
		fs      fstest.MapFS
		wantMsg string
	}{
		{
			name: "cell.yaml malformed",
			fs: fstest.MapFS{
				"cells/bad-cell/cell.yaml": &fstest.MapFile{Data: []byte(`{{{ not valid yaml`)},
			},
			wantMsg: "parse cells/bad-cell/cell.yaml",
		},
		{
			name: "slice.yaml malformed",
			fs: fstest.MapFS{
				"cells/my-cell/slices/bad-slice/slice.yaml": &fstest.MapFile{Data: []byte(`:::broken`)},
			},
			wantMsg: "parse cells/my-cell/slices/bad-slice/slice.yaml",
		},
		{
			name: "contract.yaml malformed",
			fs: fstest.MapFS{
				"contracts/http/auth/login/v1/contract.yaml": &fstest.MapFile{Data: []byte(`[[[broken`)},
			},
			wantMsg: "parse contracts/http/auth/login/v1/contract.yaml",
		},
		{
			name: "journey yaml malformed",
			fs: fstest.MapFS{
				"journeys/J-broken.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
			},
			wantMsg: "parse journeys/J-broken.yaml",
		},
		{
			name: "assembly yaml malformed",
			fs: fstest.MapFS{
				"assemblies/bad/assembly.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
			},
			wantMsg: "parse assemblies/bad/assembly.yaml",
		},
		{
			name: "status-board yaml malformed",
			fs: fstest.MapFS{
				"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
			},
			wantMsg: "parse journeys/status-board.yaml",
		},
		{
			name: "actors.yaml malformed",
			fs: fstest.MapFS{
				"actors.yaml": &fstest.MapFile{Data: []byte(`{bad`)},
			},
			wantMsg: "parse actors.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser("")
			_, err := p.ParseFS(tt.fs)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
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
		"README.md":                                &fstest.MapFile{Data: []byte(`# readme`)},
		"cells/access-core/main.go":                &fstest.MapFile{Data: []byte(`package main`)},
		"journeys/status-board.yaml.bak":           &fstest.MapFile{Data: []byte(`backup`)},
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

// TestParseFS_SliceBelongsToCellDerive is a table-driven test for G-7 auto-derivation
// of slice.belongsToCell from the file path cells/{cellID}/slices/{sliceID}/slice.yaml.
func TestParseFS_SliceBelongsToCellDerive(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		path     string
		sliceID  string
		wantCell string
	}{
		{
			name: "omitted belongsToCell is derived from path",
			path: "cells/access-core/slices/session-login/slice.yaml",
			yaml: `id: session-login
contractUsages: []
verify:
  unit: []
  contract: []
`,
			sliceID:  "session-login",
			wantCell: "access-core",
		},
		{
			name: "explicit belongsToCell matching path is preserved",
			path: "cells/audit-core/slices/write-log/slice.yaml",
			yaml: `id: write-log
belongsToCell: audit-core
contractUsages: []
verify:
  unit: []
  contract: []
`,
			sliceID:  "write-log",
			wantCell: "audit-core",
		},
		{
			name: "derived from path with simple cell name",
			path: "cells/crypto/slices/hash/slice.yaml",
			yaml: `id: hash
contractUsages: []
verify:
  unit: []
  contract: []
`,
			sliceID:  "hash",
			wantCell: "crypto",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				tt.path: &fstest.MapFile{Data: []byte(tt.yaml)},
			}
			p := NewParser("")
			pm, err := p.ParseFS(fsys)
			require.NoError(t, err)
			wantKey := tt.wantCell + "/" + tt.sliceID
			require.Len(t, pm.Slices, 1)
			assert.Contains(t, pm.Slices, wantKey)
			assert.NotContains(t, pm.Slices, "/"+tt.sliceID,
				"empty belongsToCell must not produce malformed key")
			sl := pm.Slices[wantKey]
			require.NotNil(t, sl)
			assert.Equal(t, tt.wantCell, sl.BelongsToCell)
		})
	}
}

// TestParseFS_SliceBelongsToCellMismatch verifies that an explicit belongsToCell
// that contradicts the directory path is rejected with an error.
func TestParseFS_SliceBelongsToCellMismatch(t *testing.T) {
	fsys := fstest.MapFS{
		"cells/access-core/slices/session-login/slice.yaml": &fstest.MapFile{Data: []byte(`id: session-login
belongsToCell: wrong-cell
contractUsages: []
verify:
  unit: []
  contract: []
`)},
	}
	p := NewParser("")
	_, err := p.ParseFS(fsys)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
	assert.Contains(t, err.Error(), "does not match directory")
	assert.Contains(t, err.Error(), "wrong-cell")
	assert.Contains(t, err.Error(), "access-core")
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
  entrypoint: cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
`)},
		"assemblies/core-bundle-v2/assembly.yaml": &fstest.MapFile{Data: []byte(`id: core-bundle
cells:
  - access-core
build:
  entrypoint: cmd/core-bundle/main.go
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

// Note: empty-ID validation across file kinds is covered by
// TestParseFS_EmptyStructFiles (below). The previous 5 TestParseFS_Empty*ID
// tests were redundant variants using `id: ""` instead of entirely empty
// files; both produce the same zero-struct outcome.

func TestParseFS_ContractOwnerCellInferred(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantOwner string
	}{
		{
			name: "http infers ownerCell from server",
			yaml: `id: http.test.v1
kind: http
consistencyLevel: L1
lifecycle: active
endpoints:
  server: cell-a
  clients: [cell-b]
`,
			wantOwner: "cell-a",
		},
		{
			name: "event infers ownerCell from publisher",
			yaml: `id: event.test.v1
kind: event
consistencyLevel: L2
lifecycle: active
endpoints:
  publisher: cell-b
  subscribers: [cell-a]
`,
			wantOwner: "cell-b",
		},
		{
			name: "command infers ownerCell from handler",
			yaml: `id: command.test.v1
kind: command
consistencyLevel: L1
lifecycle: active
endpoints:
  handler: cell-c
  invokers: [cell-a]
`,
			wantOwner: "cell-c",
		},
		{
			name: "projection infers ownerCell from provider",
			yaml: `id: projection.test.v1
kind: projection
consistencyLevel: L1
lifecycle: active
endpoints:
  provider: cell-d
  readers: [cell-a]
`,
			wantOwner: "cell-d",
		},
		{
			name: "explicit ownerCell is preserved",
			yaml: `id: http.explicit.v1
kind: http
ownerCell: explicit-cell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: different-cell
  clients: [cell-b]
`,
			wantOwner: "explicit-cell",
		},
		{
			name: "empty provider endpoint leaves ownerCell empty",
			yaml: `id: http.noprovider.v1
kind: http
consistencyLevel: L1
lifecycle: active
endpoints:
  clients: [cell-b]
`,
			wantOwner: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := fstest.MapFS{
				"contracts/test/domain/v1/contract.yaml": &fstest.MapFile{Data: []byte(tt.yaml)},
			}
			p := NewParser("")
			pm, err := p.ParseFS(fs)
			require.NoError(t, err)
			require.Len(t, pm.Contracts, 1)
			for _, c := range pm.Contracts {
				assert.Equal(t, tt.wantOwner, c.OwnerCell)
			}
		})
	}
}

func TestParseFS_RejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name    string
		fs      fstest.MapFS
		wantMsg string
	}{
		{
			name: "unknown field in cell.yaml",
			fs: fstest.MapFS{
				"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(`id: x
type: core
consistencyLevel: L1
owner:
  team: t
  role: r
schema:
  primary: tbl
verify:
  smoke: []
unknownField: oops
`)},
			},
			wantMsg: "unknownField",
		},
		{
			name: "unknown field in slice.yaml",
			fs: fstest.MapFS{
				"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(`id: x
type: core
consistencyLevel: L1
owner: {team: t, role: r}
schema: {primary: tbl}
verify: {smoke: []}
`)},
				"cells/x/slices/s/slice.yaml": &fstest.MapFile{Data: []byte(`id: s
belongsToCell: x
contractUsages: []
verify: {unit: [], contract: []}
typo_field: bad
`)},
			},
			wantMsg: "typo_field",
		},
		{
			name: "unknown field in contract.yaml",
			fs: fstest.MapFS{
				"contracts/http/test/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: http.test.v1
kind: http
lifecycle: active
endpoints: {server: x}
bogus: 42
`)},
			},
			wantMsg: "bogus",
		},
		{
			name: "unknown field in journey yaml",
			fs: fstest.MapFS{
				"journeys/J-test.yaml": &fstest.MapFile{Data: []byte(`id: J-test
goal: test
owner: {team: t, role: r}
cells: []
contracts: []
passCriteria: []
badField: oops
`)},
			},
			wantMsg: "badField",
		},
		{
			name: "unknown field in assembly yaml",
			fs: fstest.MapFS{
				"assemblies/a/assembly.yaml": &fstest.MapFile{Data: []byte(`id: a
cells: []
build: {entrypoint: main.go, binary: a, deployTemplate: t}
nope: true
`)},
			},
			wantMsg: "nope",
		},
		{
			name: "unknown field in status-board entry",
			fs: fstest.MapFS{
				"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(`- journeyId: J-x
  state: green
  risk: none
  blocker: ""
  updatedAt: "2026-01-01"
  extra: bad
`)},
			},
			wantMsg: "extra",
		},
		{
			name: "unknown field in actors entry",
			fs: fstest.MapFS{
				"actors.yaml": &fstest.MapFile{Data: []byte(`- id: ext
  type: external
  maxConsistencyLevel: L2
  phantom: yes
`)},
			},
			wantMsg: "phantom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(".")
			_, err := p.ParseFS(tt.fs)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr), "expected *errcode.Error, got: %T", err)
			assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

func TestParseFS_SchemaRefsExtraKeys(t *testing.T) {
	fs := fstest.MapFS{
		"contracts/http/test/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: http.test.v1
kind: http
lifecycle: active
endpoints: {server: x}
schemaRefs:
  request: req.json
  response: res.json
  custom: extra.json
`)},
	}
	p := NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)
	c := pm.Contracts["http.test.v1"]
	require.NotNil(t, c)
	assert.Equal(t, "req.json", c.SchemaRefs.Request)
	assert.Equal(t, "res.json", c.SchemaRefs.Response)
	assert.Equal(t, "extra.json", c.SchemaRefs.Extra["custom"])
}

func TestParseFS_HTTPTransportMetadata(t *testing.T) {
	fs := fstest.MapFS{
		"contracts/http/auth/user/delete/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: http.auth.user.delete.v1
kind: http
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
  http:
    method: DELETE
    path: /api/v1/auth/users/{userId}
    successStatus: 204
    noContent: true
schemaRefs:
  request: request.schema.json
`)},
	}
	p := NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)
	c := pm.Contracts["http.auth.user.delete.v1"]
	require.NotNil(t, c)
	require.NotNil(t, c.Endpoints.HTTP)
	assert.Equal(t, "DELETE", c.Endpoints.HTTP.Method)
	assert.Equal(t, "/api/v1/auth/users/{userId}", c.Endpoints.HTTP.Path)
	assert.Equal(t, 204, c.Endpoints.HTTP.SuccessStatus)
	assert.True(t, c.Endpoints.HTTP.NoContent)
}

func TestParseFS_RejectsMultipleDocuments(t *testing.T) {
	tests := []struct {
		name string
		fs   fstest.MapFS
	}{
		{
			name: "multi-doc cell.yaml",
			fs: fstest.MapFS{
				"cells/x/cell.yaml": &fstest.MapFile{Data: []byte("id: x\ntype: core\nconsistencyLevel: L1\nowner: {team: t, role: r}\nschema: {primary: tbl}\nverify: {smoke: []}\n---\nid: y\ntype: edge\n")},
			},
		},
		{
			name: "multi-doc contract.yaml",
			fs: fstest.MapFS{
				"contracts/http/test/v1/contract.yaml": &fstest.MapFile{Data: []byte("id: http.test.v1\nkind: http\nlifecycle: active\nendpoints: {server: x}\n---\nid: injected\n")},
			},
		},
		{
			name: "multi-doc actors.yaml",
			fs: fstest.MapFS{
				"actors.yaml": &fstest.MapFile{Data: []byte("- id: a\n  type: external\n  maxConsistencyLevel: L2\n---\n- id: b\n  type: external\n  maxConsistencyLevel: L3\n")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(".")
			_, err := p.ParseFS(tt.fs)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
			assert.Contains(t, err.Error(), "multiple YAML documents")
		})
	}
}

func TestParseFS_EmptyFiles(t *testing.T) {
	tests := []struct {
		name string
		fs   fstest.MapFS
	}{
		{
			name: "empty actors.yaml",
			fs: fstest.MapFS{
				"actors.yaml": &fstest.MapFile{Data: []byte("")},
			},
		},
		{
			name: "whitespace-only status-board.yaml",
			fs: fstest.MapFS{
				"journeys/status-board.yaml": &fstest.MapFile{Data: []byte("  \n")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(".")
			pm, err := p.ParseFS(tt.fs)
			require.NoError(t, err, "empty file should parse without error")
			assert.NotNil(t, pm)
		})
	}
}

// TestParseFS_EmptyStructFiles verifies that empty files for struct-based
// metadata types (cell, contract, journey, assembly) fail with "id is empty"
// rather than silently succeeding. This documents the boundary: empty list
// files (actors, status-board) are OK, but empty struct files must have an ID.
func TestParseFS_EmptyStructFiles(t *testing.T) {
	tests := []struct {
		name    string
		fs      fstest.MapFS
		wantMsg string
	}{
		{
			name: "empty cell.yaml",
			fs: fstest.MapFS{
				"cells/x/cell.yaml": &fstest.MapFile{Data: []byte("")},
			},
			wantMsg: "cell id is empty",
		},
		{
			name: "empty slice.yaml",
			fs: fstest.MapFS{
				"cells/x/slices/y/slice.yaml": &fstest.MapFile{Data: []byte("")},
			},
			wantMsg: "slice id is empty",
		},
		{
			name: "empty contract.yaml",
			fs: fstest.MapFS{
				"contracts/http/test/v1/contract.yaml": &fstest.MapFile{Data: []byte("")},
			},
			wantMsg: "contract id is empty",
		},
		{
			name: "empty journey yaml",
			fs: fstest.MapFS{
				"journeys/J-test.yaml": &fstest.MapFile{Data: []byte("")},
			},
			wantMsg: "journey id is empty",
		},
		{
			name: "empty assembly yaml",
			fs: fstest.MapFS{
				"assemblies/a/assembly.yaml": &fstest.MapFile{Data: []byte("")},
			},
			wantMsg: "assembly id is empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(".")
			_, err := p.ParseFS(tt.fs)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}
