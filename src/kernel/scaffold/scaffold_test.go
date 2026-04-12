package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// readGenerated reads the generated file and returns its content as a string.
func readGenerated(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read generated file: %s", path)
	return string(data)
}

// requireErrCode asserts that err is an *errcode.Error with the expected code.
func requireErrCode(t *testing.T, err error, code errcode.Code) {
	t.Helper()
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "expected *errcode.Error, got %T", err)
	assert.Equal(t, code, ecErr.Code)
}

// ---------------------------------------------------------------------------
// CreateCell
// ---------------------------------------------------------------------------

func TestCreateCell(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := CellOpts{
		ID:               "billing-core",
		Type:             "core",
		ConsistencyLevel: "L2",
		OwnerTeam:        "commerce",
	}

	require.NoError(t, s.CreateCell(opts))

	// Verify directory was created.
	cellDir := filepath.Join(root, "cells", "billing-core")
	info, err := os.Stat(cellDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify cell.yaml content.
	content := readGenerated(t, filepath.Join(cellDir, "cell.yaml"))
	assert.Contains(t, content, "id: billing-core")
	assert.Contains(t, content, "type: core")
	assert.Contains(t, content, "consistencyLevel: L2")
	assert.Contains(t, content, "team: commerce")
	assert.Contains(t, content, "role: cell-owner")
	assert.Contains(t, content, "primary: cell_billing_core")
	assert.Contains(t, content, "smoke.billing-core.startup")
	assert.Contains(t, content, "l0Dependencies: []")
}

func TestCreateCell_Defaults(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := CellOpts{
		ID:        "audit-core",
		OwnerTeam: "platform",
		// Type and ConsistencyLevel left empty — should use defaults.
	}

	require.NoError(t, s.CreateCell(opts))

	content := readGenerated(t, filepath.Join(root, "cells", "audit-core", "cell.yaml"))
	assert.Contains(t, content, "type: core")
	assert.Contains(t, content, "consistencyLevel: L2")
}

func TestCreateCell_Conflict(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := CellOpts{ID: "dup-cell", OwnerTeam: "team-a"}
	require.NoError(t, s.CreateCell(opts))

	// Second call must fail with conflict.
	err := s.CreateCell(opts)
	requireErrCode(t, err, ErrScaffoldConflict)
}

func TestCreateCell_MissingID(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateCell(CellOpts{OwnerTeam: "t"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateCell_MissingOwner(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateCell(CellOpts{ID: "x"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateCell_ReplaceHyphen(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	require.NoError(t, s.CreateCell(CellOpts{
		ID:        "my-long-name",
		OwnerTeam: "team",
	}))

	content := readGenerated(t, filepath.Join(root, "cells", "my-long-name", "cell.yaml"))
	assert.Contains(t, content, "primary: cell_my_long_name")
}

// ---------------------------------------------------------------------------
// CreateSlice
// ---------------------------------------------------------------------------

func TestCreateSlice(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	// Must create cell first.
	require.NoError(t, s.CreateCell(CellOpts{ID: "access-core", OwnerTeam: "platform"}))

	opts := SliceOpts{ID: "session-login", CellID: "access-core"}
	require.NoError(t, s.CreateSlice(opts))

	sliceDir := filepath.Join(root, "cells", "access-core", "slices", "session-login")
	info, err := os.Stat(sliceDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	content := readGenerated(t, filepath.Join(sliceDir, "slice.yaml"))
	assert.Contains(t, content, "id: session-login")
	assert.Contains(t, content, "belongsToCell: access-core")
	assert.Contains(t, content, "contractUsages: []")
	assert.Contains(t, content, "unit.session-login.service")
	assert.Contains(t, content, "contract: []")
	assert.Contains(t, content, "waivers: []")
}

func TestCreateSlice_CellMissing(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	err := s.CreateSlice(SliceOpts{ID: "my-slice", CellID: "nonexistent"})
	requireErrCode(t, err, ErrScaffoldCellMissing)
}

func TestCreateSlice_Conflict(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	require.NoError(t, s.CreateCell(CellOpts{ID: "c1", OwnerTeam: "t"}))

	opts := SliceOpts{ID: "s1", CellID: "c1"}
	require.NoError(t, s.CreateSlice(opts))

	err := s.CreateSlice(opts)
	requireErrCode(t, err, ErrScaffoldConflict)
}

func TestCreateSlice_MissingID(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateSlice(SliceOpts{CellID: "c1"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateSlice_MissingCellID(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateSlice(SliceOpts{ID: "s1"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

// ---------------------------------------------------------------------------
// CreateContract — HTTP
// ---------------------------------------------------------------------------

func TestCreateContractHTTP(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := ContractOpts{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		OwnerCell: "access-core",
	}
	require.NoError(t, s.CreateContract(opts))

	// Verify directory: contracts/http/auth/login/v1/
	dir := filepath.Join(root, "contracts", "http", "auth", "login", "v1")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	content := readGenerated(t, filepath.Join(dir, "contract.yaml"))
	assert.Contains(t, content, "id: http.auth.login.v1")
	assert.Contains(t, content, "kind: http")
	assert.Contains(t, content, "ownerCell: access-core")
	assert.Contains(t, content, "consistencyLevel: L1")
	assert.Contains(t, content, "lifecycle: draft")
	assert.Contains(t, content, "server: access-core")
	assert.Contains(t, content, "clients: []")
	assert.Contains(t, content, "request: request.schema.json")
	assert.Contains(t, content, "response: response.schema.json")
}

// ---------------------------------------------------------------------------
// CreateContract — Event
// ---------------------------------------------------------------------------

func TestCreateContractEvent(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := ContractOpts{
		ID:        "event.session.revoked.v1",
		Kind:      "event",
		OwnerCell: "access-core",
	}
	require.NoError(t, s.CreateContract(opts))

	dir := filepath.Join(root, "contracts", "event", "session", "revoked", "v1")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	content := readGenerated(t, filepath.Join(dir, "contract.yaml"))
	assert.Contains(t, content, "id: event.session.revoked.v1")
	assert.Contains(t, content, "kind: event")
	assert.Contains(t, content, "ownerCell: access-core")
	assert.Contains(t, content, "consistencyLevel: L2")
	assert.Contains(t, content, "replayable: true")
	assert.Contains(t, content, "idempotencyKey: event_id")
	assert.Contains(t, content, "deliverySemantics: at-least-once")
	assert.Contains(t, content, "publisher: access-core")
	assert.Contains(t, content, "subscribers: []")
}

// ---------------------------------------------------------------------------
// CreateContract — Command
// ---------------------------------------------------------------------------

func TestCreateContractCommand(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := ContractOpts{
		ID:        "command.device.enroll.v1",
		Kind:      "command",
		OwnerCell: "device-core",
	}
	require.NoError(t, s.CreateContract(opts))

	dir := filepath.Join(root, "contracts", "command", "device", "enroll", "v1")
	content := readGenerated(t, filepath.Join(dir, "contract.yaml"))
	assert.Contains(t, content, "kind: command")
	assert.Contains(t, content, "handler: device-core")
	assert.Contains(t, content, "invokers: []")
	assert.Contains(t, content, "consistencyLevel: L2")
}

// ---------------------------------------------------------------------------
// CreateContract — Projection
// ---------------------------------------------------------------------------

func TestCreateContractProjection(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := ContractOpts{
		ID:        "projection.audit.summary.v1",
		Kind:      "projection",
		OwnerCell: "audit-core",
	}
	require.NoError(t, s.CreateContract(opts))

	dir := filepath.Join(root, "contracts", "projection", "audit", "summary", "v1")
	content := readGenerated(t, filepath.Join(dir, "contract.yaml"))
	assert.Contains(t, content, "kind: projection")
	assert.Contains(t, content, "provider: audit-core")
	assert.Contains(t, content, "readers: []")
	assert.Contains(t, content, "consistencyLevel: L3")
}

// ---------------------------------------------------------------------------
// CreateContract — Validation
// ---------------------------------------------------------------------------

func TestCreateContract_InvalidKind(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateContract(ContractOpts{ID: "rpc.x.v1", Kind: "rpc", OwnerCell: "c"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateContract_MissingID(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateContract(ContractOpts{Kind: "http", OwnerCell: "c"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateContract_MissingKind(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateContract(ContractOpts{ID: "http.x.v1", OwnerCell: "c"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateContract_MissingOwnerCell(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateContract(ContractOpts{ID: "http.x.v1", Kind: "http"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateContract_IDTooShort(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateContract(ContractOpts{ID: "http.v1", Kind: "http", OwnerCell: "c"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateContract_IDPrefixMismatchKind(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateContract(ContractOpts{ID: "event.auth.login.v1", Kind: "http", OwnerCell: "c"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
	assert.Contains(t, err.Error(), "prefix")
	assert.Contains(t, err.Error(), "must match kind")
}

func TestCreateContract_Conflict(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := ContractOpts{ID: "http.auth.user.create.v1", Kind: "http", OwnerCell: "access-core"}
	require.NoError(t, s.CreateContract(opts))

	err := s.CreateContract(opts)
	requireErrCode(t, err, ErrScaffoldConflict)
}

// ---------------------------------------------------------------------------
// Contract ID parsing — directory path
// ---------------------------------------------------------------------------

func TestCreateContract_IDParsing(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		kind    string
		wantDir string // relative to root
	}{
		{
			name:    "http 4 segments",
			id:      "http.auth.login.v1",
			kind:    "http",
			wantDir: "contracts/http/auth/login/v1",
		},
		{
			name:    "event 4 segments",
			id:      "event.session.created.v1",
			kind:    "event",
			wantDir: "contracts/event/session/created/v1",
		},
		{
			name:    "command 4 segments",
			id:      "command.device.enroll.v1",
			kind:    "command",
			wantDir: "contracts/command/device/enroll/v1",
		},
		{
			name:    "projection 4 segments",
			id:      "projection.audit.summary.v1",
			kind:    "projection",
			wantDir: "contracts/projection/audit/summary/v1",
		},
		{
			name:    "deep path 5 segments",
			id:      "http.auth.mfa.verify.v2",
			kind:    "http",
			wantDir: "contracts/http/auth/mfa/verify/v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			s := New(root)

			err := s.CreateContract(ContractOpts{
				ID:        tt.id,
				Kind:      tt.kind,
				OwnerCell: "test-cell",
			})
			require.NoError(t, err)

			expectedDir := filepath.Join(root, filepath.FromSlash(tt.wantDir))
			_, statErr := os.Stat(filepath.Join(expectedDir, "contract.yaml"))
			require.NoError(t, statErr, "expected contract.yaml at %s", expectedDir)
		})
	}
}

// ---------------------------------------------------------------------------
// CreateJourney
// ---------------------------------------------------------------------------

func TestCreateJourney(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := JourneyOpts{
		ID:        "sso-login",
		Goal:      "User completes SSO login and obtains a valid session",
		OwnerTeam: "platform",
		Cells:     []string{"access-core", "audit-core"},
	}
	require.NoError(t, s.CreateJourney(opts))

	outPath := filepath.Join(root, "journeys", "J-sso-login.yaml")
	content := readGenerated(t, outPath)
	assert.Contains(t, content, "id: J-sso-login")
	assert.Contains(t, content, `goal: "User completes SSO login and obtains a valid session"`)
	assert.Contains(t, content, "team: platform")
	assert.Contains(t, content, "role: journey-owner")
	assert.Contains(t, content, "- access-core")
	assert.Contains(t, content, "- audit-core")
	assert.Contains(t, content, "contracts: []")
	assert.Contains(t, content, "passCriteria: []")
}

func TestCreateJourney_WithJPrefix(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := JourneyOpts{
		ID:        "J-config-reload",
		Goal:      "Config hot reload works",
		OwnerTeam: "platform",
		Cells:     []string{"config-core"},
	}
	require.NoError(t, s.CreateJourney(opts))

	// Should NOT double-prefix: file is J-config-reload.yaml, not J-J-config-reload.yaml.
	outPath := filepath.Join(root, "journeys", "J-config-reload.yaml")
	_, err := os.Stat(outPath)
	require.NoError(t, err, "expected file at %s", outPath)

	content := readGenerated(t, outPath)
	assert.Contains(t, content, "id: J-config-reload")
}

func TestCreateJourney_Conflict(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	opts := JourneyOpts{ID: "dup", Goal: "g", OwnerTeam: "t", Cells: []string{"c"}}
	require.NoError(t, s.CreateJourney(opts))

	err := s.CreateJourney(opts)
	requireErrCode(t, err, ErrScaffoldConflict)
}

func TestCreateJourney_MissingID(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateJourney(JourneyOpts{Goal: "g", OwnerTeam: "t", Cells: []string{"c"}})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateJourney_MissingGoal(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateJourney(JourneyOpts{ID: "j", OwnerTeam: "t", Cells: []string{"c"}})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateJourney_MissingOwner(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateJourney(JourneyOpts{ID: "j", Goal: "g", Cells: []string{"c"}})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

func TestCreateJourney_NoCells(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	err := s.CreateJourney(JourneyOpts{ID: "j", Goal: "g", OwnerTeam: "t"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

// ---------------------------------------------------------------------------
// Path traversal prevention
// ---------------------------------------------------------------------------

func TestPathTraversal(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	cases := []struct {
		name string
		fn   func() error
	}{
		{"cell ../etc", func() error {
			return s.CreateCell(CellOpts{ID: "../etc", OwnerTeam: "t"})
		}},
		{"cell foo/bar", func() error {
			return s.CreateCell(CellOpts{ID: "foo/bar", OwnerTeam: "t"})
		}},
		{`cell foo\bar`, func() error {
			return s.CreateCell(CellOpts{ID: `foo\bar`, OwnerTeam: "t"})
		}},
		{"slice ../x", func() error {
			return s.CreateSlice(SliceOpts{ID: "../x", CellID: "c"})
		}},
		{"slice cellID ../c", func() error {
			return s.CreateSlice(SliceOpts{ID: "s", CellID: "../c"})
		}},
		{"contract ownerCell ../c", func() error {
			return s.CreateContract(ContractOpts{ID: "http.a.v1", Kind: "http", OwnerCell: "../c"})
		}},
		{"contract ID segment ..", func() error {
			return s.CreateContract(ContractOpts{ID: "http..exploit.v1", Kind: "http", OwnerCell: "c"})
		}},
		{"journey ../admin", func() error {
			return s.CreateJourney(JourneyOpts{ID: "../admin", Goal: "g", OwnerTeam: "t", Cells: []string{"c"}})
		}},
		{"cell dot", func() error {
			return s.CreateCell(CellOpts{ID: ".", OwnerTeam: "t"})
		}},
		{"slice cellID dot", func() error {
			return s.CreateSlice(SliceOpts{ID: "s", CellID: "."})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			requireErrCode(t, err, ErrScaffoldInvalidOpts)
		})
	}
}

// ---------------------------------------------------------------------------
// Template embedding
// ---------------------------------------------------------------------------

func TestTemplateFS_ContainsAllTemplates(t *testing.T) {
	expected := []string{
		"templates/cell.yaml.tpl",
		"templates/slice.yaml.tpl",
		"templates/contract-http.yaml.tpl",
		"templates/contract-event.yaml.tpl",
		"templates/contract-command.yaml.tpl",
		"templates/contract-projection.yaml.tpl",
		"templates/journey.yaml.tpl",
	}

	for _, name := range expected {
		data, err := templateFS.ReadFile(name)
		require.NoError(t, err, "template %s should be embedded", name)
		assert.True(t, len(data) > 0, "template %s should not be empty", name)
	}
}

// ---------------------------------------------------------------------------
// Integration: full cell + slice + contract flow
// ---------------------------------------------------------------------------

func TestIntegration_CellSliceContractJourney(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	// 1. Create cell.
	require.NoError(t, s.CreateCell(CellOpts{
		ID:               "order-core",
		Type:             "core",
		ConsistencyLevel: "L2",
		OwnerTeam:        "commerce",
	}))

	// 2. Create two slices.
	require.NoError(t, s.CreateSlice(SliceOpts{ID: "order-create", CellID: "order-core"}))
	require.NoError(t, s.CreateSlice(SliceOpts{ID: "order-cancel", CellID: "order-core"}))

	// 3. Create contracts.
	require.NoError(t, s.CreateContract(ContractOpts{
		ID: "http.order.create.v1", Kind: "http", OwnerCell: "order-core",
	}))
	require.NoError(t, s.CreateContract(ContractOpts{
		ID: "event.order.created.v1", Kind: "event", OwnerCell: "order-core",
	}))

	// 4. Create journey.
	require.NoError(t, s.CreateJourney(JourneyOpts{
		ID:        "order-checkout",
		Goal:      "Complete checkout flow",
		OwnerTeam: "commerce",
		Cells:     []string{"order-core", "billing-core"},
	}))

	// Verify all files exist.
	paths := []string{
		"cells/order-core/cell.yaml",
		"cells/order-core/slices/order-create/slice.yaml",
		"cells/order-core/slices/order-cancel/slice.yaml",
		"contracts/http/order/create/v1/contract.yaml",
		"contracts/event/order/created/v1/contract.yaml",
		"journeys/J-order-checkout.yaml",
	}
	for _, p := range paths {
		full := filepath.Join(root, p)
		_, err := os.Stat(full)
		require.NoError(t, err, "expected file: %s", p)
	}

	// Spot-check content consistency.
	cellContent := readGenerated(t, filepath.Join(root, "cells/order-core/cell.yaml"))
	assert.True(t, strings.Contains(cellContent, "id: order-core"))

	sliceContent := readGenerated(t, filepath.Join(root, "cells/order-core/slices/order-create/slice.yaml"))
	assert.True(t, strings.Contains(sliceContent, "belongsToCell: order-core"))

	contractContent := readGenerated(t, filepath.Join(root, "contracts/event/order/created/v1/contract.yaml"))
	assert.True(t, strings.Contains(contractContent, "publisher: order-core"))

	journeyContent := readGenerated(t, filepath.Join(root, "journeys/J-order-checkout.yaml"))
	assert.True(t, strings.Contains(journeyContent, "- order-core"))
	assert.True(t, strings.Contains(journeyContent, "- billing-core"))
}
