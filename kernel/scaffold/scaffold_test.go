package scaffold

import (
	"errors"
	"go/parser"
	"go/token"
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
	data, err := os.ReadFile(filepath.Clean(path))
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
		ID:        "auditcore",
		OwnerTeam: "platform",
		// Type and ConsistencyLevel left empty — should use defaults.
	}

	require.NoError(t, s.CreateCell(opts))

	content := readGenerated(t, filepath.Join(root, "cells", "auditcore", "cell.yaml"))
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
	require.NoError(t, s.CreateCell(CellOpts{ID: "accesscore", OwnerTeam: "platform"}))

	opts := SliceOpts{ID: "sessionlogin", CellID: "accesscore"}
	require.NoError(t, s.CreateSlice(opts))

	sliceDir := filepath.Join(root, "cells", "accesscore", "slices", "sessionlogin")
	info, err := os.Stat(sliceDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	content := readGenerated(t, filepath.Join(sliceDir, "slice.yaml"))
	assert.Contains(t, content, "id: sessionlogin")
	assert.Contains(t, content, "belongsToCell: accesscore")
	assert.Contains(t, content, "contractUsages: []")
	assert.Contains(t, content, "unit.sessionlogin.service")
	assert.Contains(t, content, "contract: []")
	assert.Contains(t, content, "waivers: []")

	// PR-A45 / PR239-T1: scaffold must also emit a handler.go that already
	// uses the canonical UUID path-param validation pattern (httputil.ParseUUIDPathParam).
	// CH-05 enforces this convention for any contract declaring `pathParams.{name}.format: uuid`,
	// so the scaffolded slice must lead developers into the correct shape from the start.
	handlerPath := filepath.Join(sliceDir, "handler.go")
	handlerContent := readGenerated(t, handlerPath)
	assert.Contains(t, handlerContent, "package sessionlogin",
		"handler package must match slice ID")
	assert.Contains(t, handlerContent, "httputil.ParseUUIDPathParam(w, r,",
		"handler must contain the UUID path-param validation boilerplate (CH-05)")
	assert.Contains(t, handlerContent, "github.com/ghbvf/gocell/pkg/httputil",
		"handler must import pkg/httputil")
}

func TestCreateSlice_CellMissing(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	err := s.CreateSlice(SliceOpts{ID: "myslice", CellID: "nonexistent"})
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
		OwnerCell: "accesscore",
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
	assert.Contains(t, content, "ownerCell: accesscore")
	assert.Contains(t, content, "consistencyLevel: L1")
	assert.Contains(t, content, "lifecycle: draft")
	assert.Contains(t, content, "server: accesscore")
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
		OwnerCell: "accesscore",
	}
	require.NoError(t, s.CreateContract(opts))

	dir := filepath.Join(root, "contracts", "event", "session", "revoked", "v1")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	content := readGenerated(t, filepath.Join(dir, "contract.yaml"))
	assert.Contains(t, content, "id: event.session.revoked.v1")
	assert.Contains(t, content, "kind: event")
	assert.Contains(t, content, "ownerCell: accesscore")
	assert.Contains(t, content, "consistencyLevel: L2")
	assert.Contains(t, content, "replayable: true")
	assert.Contains(t, content, "idempotencyKey: event_id")
	assert.Contains(t, content, "deliverySemantics: at-least-once")
	assert.Contains(t, content, "publisher: accesscore")
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
		OwnerCell: "auditcore",
	}
	require.NoError(t, s.CreateContract(opts))

	dir := filepath.Join(root, "contracts", "projection", "audit", "summary", "v1")
	content := readGenerated(t, filepath.Join(dir, "contract.yaml"))
	assert.Contains(t, content, "kind: projection")
	assert.Contains(t, content, "provider: auditcore")
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

	opts := ContractOpts{ID: "http.auth.user.create.v1", Kind: "http", OwnerCell: "accesscore"}
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
		ID:        "ssologin",
		Goal:      "User completes SSO login and obtains a valid session",
		OwnerTeam: "platform",
		Cells:     []string{"accesscore", "auditcore"},
	}
	require.NoError(t, s.CreateJourney(opts))

	outPath := filepath.Join(root, "journeys", "J-ssologin.yaml")
	content := readGenerated(t, outPath)
	assert.Contains(t, content, "id: J-ssologin")
	assert.Contains(t, content, `goal: "User completes SSO login and obtains a valid session"`)
	assert.Contains(t, content, "team: platform")
	assert.Contains(t, content, "role: journey-owner")
	assert.Contains(t, content, "lifecycle: experimental")
	assert.Contains(t, content, "- accesscore")
	assert.Contains(t, content, "- auditcore")
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
		Cells:     []string{"configcore"},
	}
	require.NoError(t, s.CreateJourney(opts))

	// Should NOT double-prefix the J- namespace, AND should strip secondary
	// dashes in the name portion: J-config-reload normalizes to J-configreload
	// so generated ids satisfy the no-dash naming convention.
	outPath := filepath.Join(root, "journeys", "J-configreload.yaml")
	_, err := os.Stat(outPath)
	require.NoError(t, err, "expected file at %s", outPath)

	content := readGenerated(t, outPath)
	assert.Contains(t, content, "id: J-configreload")

	// Old dashed filename must NOT be created.
	oldPath := filepath.Join(root, "journeys", "J-config-reload.yaml")
	_, err = os.Stat(oldPath)
	require.Error(t, err, "dashed filename %s should not exist after normalization", oldPath)
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
// WithDryRun
// ---------------------------------------------------------------------------

// TestScaffolder_WithDryRun_NoFileWritten verifies that dry-run mode skips
// all filesystem writes while still returning nil on valid opts.
func TestScaffolder_WithDryRun_NoFileWritten(t *testing.T) {
	root := t.TempDir()
	s := New(root).WithDryRun(true)

	require.NoError(t, s.CreateCell(CellOpts{ID: "dry-cell", OwnerTeam: "squad"}))

	_, err := os.Stat(filepath.Join(root, "cells", "dry-cell", "cell.yaml"))
	assert.True(t, os.IsNotExist(err), "dry-run must not write cell.yaml")
}

// TestScaffolder_WithDryRun_StillReportsConflict verifies that a pre-existing
// target path causes ErrScaffoldConflict even when dryRun is true.
func TestScaffolder_WithDryRun_StillReportsConflict(t *testing.T) {
	root := t.TempDir()

	// Create the target that would conflict.
	cellDir := filepath.Join(root, "cells", "exists-cell")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"),
		[]byte("id: exists-cell\n"), 0o644))

	s := New(root).WithDryRun(true)
	err := s.CreateCell(CellOpts{ID: "exists-cell", OwnerTeam: "squad"})
	requireErrCode(t, err, ErrScaffoldConflict)
}

// TestScaffolder_WithDryRun_StillValidatesOpts verifies that invalid opts
// (missing required fields) are still rejected in dry-run mode before any I/O.
func TestScaffolder_WithDryRun_StillValidatesOpts(t *testing.T) {
	root := t.TempDir()
	s := New(root).WithDryRun(true)

	// Missing OwnerTeam — must fail regardless of dryRun.
	err := s.CreateCell(CellOpts{ID: "no-team"})
	requireErrCode(t, err, ErrScaffoldInvalidOpts)
}

// ---------------------------------------------------------------------------
// Template embedding
// ---------------------------------------------------------------------------

func TestTemplateFS_ContainsAllTemplates(t *testing.T) {
	expected := []string{
		"templates/cell.yaml.tpl",
		"templates/slice.yaml.tpl",
		"templates/handler.go.tpl",
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
// Scaffold smoke test (PR-A45 / PR239-T1 / PR239-T2)
//
// Verifies two contracts of the scaffolded slice:
//   1. The generated handler.go is syntactically valid Go (parses cleanly)
//      and contains the canonical UUID path-param validation call. This
//      protects against template drift that would otherwise only be caught
//      at slice-population time, days after scaffold runs.
//   2. The generated contract.yaml carries an intentional TODO placeholder
//      path "/api/v1/TODO/{id}" that FMT-13 (path ↔ pathParams cross-check)
//      WILL reject — this proves the placeholder mechanism works without
//      requiring scaffold output to be commit-ready.
// ---------------------------------------------------------------------------

func TestScaffoldSlice_HandlerParsesAndContainsParseUUIDPathParam(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	require.NoError(t, s.CreateCell(CellOpts{ID: "smoketest", OwnerTeam: "platform"}))
	require.NoError(t, s.CreateSlice(SliceOpts{ID: "smokeslice", CellID: "smoketest"}))

	handlerPath := filepath.Join(root, "cells", "smoketest", "slices", "smokeslice", "handler.go")

	// Parse the generated handler.go — catches template syntax errors at
	// scaffold time rather than waiting for a developer to copy-edit the
	// stub. This is cheaper than running `go build` (which would need a
	// synthesized go.mod) but still catches the failure mode that PR239-T1
	// cares about: scaffold emits non-compiling Go.
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, handlerPath, nil, parser.ParseComments)
	require.NoError(t, err, "scaffolded handler.go must parse as valid Go")

	content := readGenerated(t, handlerPath)
	assert.Contains(t, content, "ParseUUIDPathParam(w, r, \"id\")",
		"scaffolded handler.go must call httputil.ParseUUIDPathParam (CH-05 invariant)")
}

func TestScaffoldContract_HTTPPlaceholderPathHasIDToken(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	require.NoError(t, s.CreateCell(CellOpts{ID: "smoketest", OwnerTeam: "platform"}))
	require.NoError(t, s.CreateContract(ContractOpts{
		ID: "http.smoke.create.v1", Kind: "http", OwnerCell: "smoketest",
	}))

	contractPath := filepath.Join(root, "contracts", "http", "smoke", "create", "v1", "contract.yaml")
	content := readGenerated(t, contractPath)

	// PR239-T2: the template ships a deliberate placeholder path that exposes
	// an `{id}` token without a matching pathParams declaration so that
	// FMT-13 (governance) rejects it on first `gocell validate --strict`,
	// forcing the developer to author a real contract instead of leaving
	// scaffold output in place.
	assert.Contains(t, content, "/api/v1/TODO/{id}",
		"http contract template must keep the TODO placeholder so FMT-13 catches unfinished scaffolds")
	assert.NotContains(t, content, "pathParams:\n      id:",
		"placeholder must NOT pre-declare pathParams.id, otherwise FMT-13 misses the TODO signal")
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

	// 2. Create two slices (no-dash IDs required).
	require.NoError(t, s.CreateSlice(SliceOpts{ID: "ordercreate", CellID: "order-core"}))
	require.NoError(t, s.CreateSlice(SliceOpts{ID: "ordercancel", CellID: "order-core"}))

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
		"cells/order-core/slices/ordercreate/slice.yaml",
		"cells/order-core/slices/ordercreate/handler.go",
		"cells/order-core/slices/ordercancel/slice.yaml",
		"cells/order-core/slices/ordercancel/handler.go",
		"contracts/http/order/create/v1/contract.yaml",
		"contracts/event/order/created/v1/contract.yaml",
		"journeys/J-ordercheckout.yaml",
	}
	for _, p := range paths {
		full := filepath.Join(root, p)
		_, err := os.Stat(full)
		require.NoError(t, err, "expected file: %s", p)
	}

	// Spot-check content consistency.
	cellContent := readGenerated(t, filepath.Join(root, "cells", "order-core", "cell.yaml"))
	assert.True(t, strings.Contains(cellContent, "id: order-core"))

	sliceContent := readGenerated(t, filepath.Join(root, "cells", "order-core", "slices", "ordercreate", "slice.yaml"))
	assert.True(t, strings.Contains(sliceContent, "belongsToCell: order-core"))

	contractContent := readGenerated(t, filepath.Join(root, "contracts", "event", "order", "created", "v1", "contract.yaml"))
	assert.True(t, strings.Contains(contractContent, "publisher: order-core"))

	journeyContent := readGenerated(t, filepath.Join(root, "journeys", "J-ordercheckout.yaml"))
	assert.True(t, strings.Contains(journeyContent, "- order-core"))
	assert.True(t, strings.Contains(journeyContent, "- billing-core"))
}
