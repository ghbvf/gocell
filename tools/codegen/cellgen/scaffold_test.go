package cellgen

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
)

// TestScaffoldCell_GeneratesFiles verifies that ScaffoldCell creates both
// cell.go and cell.yaml under the target directory.
func TestScaffoldCell_GeneratesFiles(t *testing.T) {
	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     "foocell",
		StructName: "FooCell",
		Package:    "foocell",
		ModulePath: "github.com/example/myproject",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
	}

	err := ScaffoldCell(dir, "cells/foocell", spec)
	if err != nil {
		t.Fatalf("ScaffoldCell() error = %v", err)
	}

	cellGoPath := filepath.Join(dir, "cells", "foocell", "cell.go")
	cellYAMLPath := filepath.Join(dir, "cells", "foocell", "cell.yaml")

	if _, err := os.Stat(cellGoPath); err != nil {
		t.Errorf("cell.go not created: %v", err)
	}
	if _, err := os.Stat(cellYAMLPath); err != nil {
		t.Errorf("cell.yaml not created: %v", err)
	}
}

// TestScaffoldCell_CellGoContainsListenerMarker verifies the scaffolded
// cell.go includes the K#05 // +cell:listener: stub marker.
func TestScaffoldCell_CellGoContainsListenerMarker(t *testing.T) {
	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     "barcell",
		StructName: "BarCell",
		Package:    "barcell",
		ModulePath: "github.com/example/myproject",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
	}

	if err := ScaffoldCell(dir, "cells/barcell", spec); err != nil {
		t.Fatalf("ScaffoldCell() error = %v", err)
	}

	content := fileutil.MustReadFile(t, filepath.Join(dir, "cells", "barcell", "cell.go"))

	if !strings.Contains(string(content), "// +cell:listener:") {
		t.Error("cell.go missing // +cell:listener: stub marker")
	}
}

// TestScaffoldCell_CellYAMLContainsGoStructName verifies the scaffolded
// cell.yaml includes the goStructName field required by K#04 codegen.
func TestScaffoldCell_CellYAMLContainsGoStructName(t *testing.T) {
	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     "bazcore",
		StructName: "BazCore",
		Package:    "bazcore",
		ModulePath: "github.com/example/myproject",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
	}

	if err := ScaffoldCell(dir, "cells/bazcore", spec); err != nil {
		t.Fatalf("ScaffoldCell() error = %v", err)
	}

	content := fileutil.MustReadFile(t, filepath.Join(dir, "cells", "bazcore", "cell.yaml"))

	if !strings.Contains(string(content), "goStructName:") {
		t.Error("cell.yaml missing goStructName field")
	}
	if !strings.Contains(string(content), "BazCore") {
		t.Error("cell.yaml goStructName does not contain StructName")
	}
}

// TestScaffoldCell_CellYAMLContainsOwnerRole verifies the scaffolded cell.yaml
// includes the owner.role field from OwnerRole (K05-11: no TODO placeholder).
func TestScaffoldCell_CellYAMLContainsOwnerRole(t *testing.T) {
	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     "rolecell",
		StructName: "RoleCell",
		Package:    "rolecell",
		ModulePath: "github.com/example/myproject",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
	}

	if err := ScaffoldCell(dir, "cells/rolecell", spec); err != nil {
		t.Fatalf("ScaffoldCell() error = %v", err)
	}

	content := fileutil.MustReadFile(t, filepath.Join(dir, "cells", "rolecell", "cell.yaml"))

	if !strings.Contains(string(content), "role: cell-owner") {
		t.Errorf("cell.yaml should contain 'role: cell-owner', got:\n%s", content)
	}
	if strings.Contains(string(content), "role: TODO") {
		t.Error("cell.yaml must not contain 'role: TODO' placeholder")
	}
}

// TestScaffoldCell_TableDriven exercises multiple CellID/StructName combinations
// to verify template rendering correctness.
func TestScaffoldCell_TableDriven(t *testing.T) {
	tests := []struct {
		name           string
		spec           ScaffoldSpec
		wantInCellGo   []string
		wantInCellYAML []string
	}{
		{
			name: "basic cell",
			spec: ScaffoldSpec{
				CellID:     "mycore",
				StructName: "MyCore",
				Package:    "mycore",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
			},
			wantInCellGo: []string{
				"package mycore",
				"type MyCore struct",
				"func NewMyCore()",
				"// +cell:listener:",
				"func (c *MyCore) initInternal(",
				"loadCellMetadata()",
				"github.com/example/app/kernel/cell",
			},
			wantInCellYAML: []string{
				"id: mycore",
				"goStructName: MyCore",
				"smoke.mycore.startup",
				"role: cell-owner",
			},
		},
		{
			name: "cell with hyphenated-like naming avoided",
			spec: ScaffoldSpec{
				CellID:     "iotdevice",
				StructName: "IoTDevice",
				Package:    "iotdevice",
				ModulePath: "github.com/acme/iot",
				OwnerTeam:  "iot-team",
				OwnerRole:  "cell-owner",
			},
			wantInCellGo: []string{
				"package iotdevice",
				"type IoTDevice struct",
				"func NewIoTDevice()",
				"// +slice:subscribe:",
			},
			wantInCellYAML: []string{
				"id: iotdevice",
				"goStructName: IoTDevice",
				"team: iot-team",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			targetDir := filepath.Join("cells", tc.spec.CellID)

			if err := ScaffoldCell(dir, targetDir, tc.spec); err != nil {
				t.Fatalf("ScaffoldCell() error = %v", err)
			}

			cellGo := fileutil.MustReadFile(t, filepath.Join(dir, targetDir, "cell.go"))
			cellYAML := fileutil.MustReadFile(t, filepath.Join(dir, targetDir, "cell.yaml"))

			for _, want := range tc.wantInCellGo {
				if !strings.Contains(string(cellGo), want) {
					t.Errorf("cell.go missing %q", want)
				}
			}
			for _, want := range tc.wantInCellYAML {
				if !strings.Contains(string(cellYAML), want) {
					t.Errorf("cell.yaml missing %q", want)
				}
			}
		})
	}
}

// TestScaffoldCell_TypeAndLevelRendered verifies that non-default Type and
// ConsistencyLevel values are rendered into cell.yaml (DX-02).
func TestScaffoldCell_TypeAndLevelRendered(t *testing.T) {
	tests := []struct {
		name      string
		cellType  string
		level     string
		wantType  string
		wantLevel string
	}{
		{
			name:      "explicit edge L0",
			cellType:  "edge",
			level:     "L0",
			wantType:  "type: edge",
			wantLevel: "consistencyLevel: L0",
		},
		{
			name:      "explicit support L4",
			cellType:  "support",
			level:     "L4",
			wantType:  "type: support",
			wantLevel: "consistencyLevel: L4",
		},
		{
			name:      "defaults when empty",
			cellType:  "",
			level:     "",
			wantType:  "type: core",
			wantLevel: "consistencyLevel: L1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := ScaffoldSpec{
				CellID:           "typecell",
				StructName:       "TypeCell",
				Package:          "typecell",
				ModulePath:       "github.com/example/app",
				OwnerTeam:        "platform",
				OwnerRole:        "cell-owner",
				Type:             tc.cellType,
				ConsistencyLevel: tc.level,
			}
			if err := ScaffoldCell(dir, "cells/typecell", spec); err != nil {
				t.Fatalf("ScaffoldCell() error = %v", err)
			}
			content := fileutil.MustReadFile(t, filepath.Join(dir, "cells", "typecell", "cell.yaml"))
			if !strings.Contains(string(content), tc.wantType) {
				t.Errorf("cell.yaml missing %q, got:\n%s", tc.wantType, content)
			}
			if !strings.Contains(string(content), tc.wantLevel) {
				t.Errorf("cell.yaml missing %q, got:\n%s", tc.wantLevel, content)
			}
		})
	}
}

// TestScaffoldCell_TypeWhitelistRejected verifies that invalid --type values
// are rejected with a "must be one of" error (K05-05).
func TestScaffoldCell_TypeWhitelistRejected(t *testing.T) {
	tests := []struct {
		name     string
		cellType string
	}{
		{"unknown type processor", "processor"},
		{"unknown type gateway", "gateway"},
		{"unknown type fabric", "fabric"},
		{"empty-like invalid type CORE", "CORE"},
		{"injection attempt", "core\ninjected: true"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := ScaffoldSpec{
				CellID:     "typecell",
				StructName: "TypeCell",
				Package:    "typecell",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
				Type:       tc.cellType,
			}
			err := ScaffoldCell(dir, "cells/typecell", spec)
			if err == nil {
				t.Fatalf("expected error for type %q, got nil", tc.cellType)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != errcode.ErrValidationFailed {
				t.Errorf("expected errcode.ErrValidationFailed, got %q", ec.Code)
			}
		})
	}
}

// TestScaffoldCell_LevelWhitelistRejected verifies that invalid --level values
// are rejected with a "must be one of" error (K05-05).
func TestScaffoldCell_LevelWhitelistRejected(t *testing.T) {
	tests := []struct {
		name  string
		level string
	}{
		{"unknown level L5", "L5"},
		{"unknown level L-1", "L-1"},
		{"lowercase l0", "l0"},
		{"plain number 1", "1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := ScaffoldSpec{
				CellID:           "levelcell",
				StructName:       "LevelCell",
				Package:          "levelcell",
				ModulePath:       "github.com/example/app",
				OwnerTeam:        "platform",
				OwnerRole:        "cell-owner",
				ConsistencyLevel: tc.level,
			}
			err := ScaffoldCell(dir, "cells/levelcell", spec)
			if err == nil {
				t.Fatalf("expected error for level %q, got nil", tc.level)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != errcode.ErrValidationFailed {
				t.Errorf("expected errcode.ErrValidationFailed, got %q", ec.Code)
			}
		})
	}
}

// TestScaffoldCell_ConflictError verifies that ScaffoldCell returns an error
// when output files already exist (skip-on-conflict).
func TestScaffoldCell_ConflictError(t *testing.T) {
	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     "existing",
		StructName: "Existing",
		Package:    "existing",
		ModulePath: "github.com/example/app",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
	}

	// First call succeeds.
	if err := ScaffoldCell(dir, "cells/existing", spec); err != nil {
		t.Fatalf("first ScaffoldCell() error = %v", err)
	}

	// Second call must return a conflict error.
	err := ScaffoldCell(dir, "cells/existing", spec)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

// TestScaffoldCell_DryRun verifies that ScaffoldCell with DryRun=true renders
// templates (catching template/input errors) but does not write any files.
func TestScaffoldCell_DryRun(t *testing.T) {
	t.Run("no files written when no conflict", func(t *testing.T) {
		dir := t.TempDir()
		spec := ScaffoldSpec{
			CellID:     "drycell",
			StructName: "DryCell",
			Package:    "drycell",
			ModulePath: "github.com/example/app",
			OwnerTeam:  "platform",
			OwnerRole:  "cell-owner",
			DryRun:     true,
		}
		if err := ScaffoldCell(dir, "cells/drycell", spec); err != nil {
			t.Fatalf("ScaffoldCell DryRun: %v", err)
		}
		cellGoPath := filepath.Join(dir, "cells", "drycell", "cell.go")
		if _, err := os.Stat(cellGoPath); err == nil {
			t.Error("DryRun=true must not write cell.go")
		}
	})

	t.Run("conflict detected in dry-run", func(t *testing.T) {
		dir := t.TempDir()
		spec := ScaffoldSpec{
			CellID:     "conflictcell",
			StructName: "ConflictCell",
			Package:    "conflictcell",
			ModulePath: "github.com/example/app",
			OwnerTeam:  "platform",
			OwnerRole:  "cell-owner",
		}
		// First live call creates files.
		if err := ScaffoldCell(dir, "cells/conflictcell", spec); err != nil {
			t.Fatalf("first ScaffoldCell: %v", err)
		}
		// Second call with DryRun must still detect conflict.
		spec.DryRun = true
		err := ScaffoldCell(dir, "cells/conflictcell", spec)
		if err == nil {
			t.Fatal("expected conflict error in dry-run, got nil")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected 'already exists' in error, got: %v", err)
		}
	})

	t.Run("dry-run renders templates and validates cell.go syntax", func(t *testing.T) {
		dir := t.TempDir()
		spec := ScaffoldSpec{
			CellID:     "rendercell",
			StructName: "RenderCell",
			Package:    "rendercell",
			ModulePath: "github.com/example/app",
			OwnerTeam:  "platform",
			OwnerRole:  "cell-owner",
			DryRun:     true,
		}
		// Dry-run must succeed (templates are valid).
		if err := ScaffoldCell(dir, "cells/rendercell", spec); err != nil {
			t.Fatalf("DryRun render validation failed: %v", err)
		}
		// No files must be written.
		if _, err := os.Stat(filepath.Join(dir, "cells", "rendercell", "cell.go")); err == nil {
			t.Error("DryRun=true must not write cell.go")
		}
		if _, err := os.Stat(filepath.Join(dir, "cells", "rendercell", "cell.yaml")); err == nil {
			t.Error("DryRun=true must not write cell.yaml")
		}
	})
}

// TestScaffoldCell_RejectsSymlinkBreakout verifies that ScaffoldCell refuses
// to write files when an intermediate path component is a symlink pointing
// outside the repository root.
func TestScaffoldCell_RejectsSymlinkBreakout(t *testing.T) {
	root := t.TempDir()
	// outsideDir simulates an attacker-controlled directory outside the repo root.
	outsideDir := t.TempDir()

	// Place a symlink cells/ → outsideDir inside the repo root, mimicking a
	// pre-committed malicious symlink.
	symlinkPath := filepath.Join(root, "cells")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	err := ScaffoldCell(root, "cells/evil", ScaffoldSpec{
		CellID:     "evil",
		StructName: "Evil",
		Package:    "evil",
		ModulePath: "example.com/test",
		OwnerTeam:  "test",
		OwnerRole:  "cell-owner",
	})

	if err == nil {
		t.Fatal("expected symlink breakout to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "escapes root") && !strings.Contains(err.Error(), "resolves outside root") {
		t.Errorf("expected error about escaping root, got: %v", err)
	}

	// Verify nothing was written to the external directory.
	entries, readErr := os.ReadDir(outsideDir)
	if readErr != nil {
		t.Fatalf("read outsideDir: %v", readErr)
	}
	if len(entries) > 0 {
		t.Errorf("scaffold wrote %d entries to outsideDir via symlink; expected 0", len(entries))
	}
}

// TestScaffoldCell_ValidationErrors verifies that missing required fields
// are rejected before any filesystem operation. Errors are checked via
// errcode structured assertions (errcode.As + Code/Details) per the
// MESSAGE-CONST-LITERAL-01 constraint: runtime field names are in Details,
// not in the const-literal message.
func TestScaffoldCell_ValidationErrors(t *testing.T) {
	// hasDetail reports whether the errcode.Error's Details contain an attr
	// with the given key and value.
	hasDetail := func(ec *errcode.Error, key, value string) bool {
		for _, attr := range ec.Details {
			if attr.Key == key && attr.Value.String() == value {
				return true
			}
		}
		return false
	}

	// hasDetailKey reports whether any detail attr matches the given key.
	hasDetailKey := func(ec *errcode.Error, key string) bool {
		for _, attr := range ec.Details {
			if attr.Key == key {
				return true
			}
		}
		return false
	}

	tests := []struct {
		name     string
		spec     ScaffoldSpec
		wantCode errcode.Code
		checkErr func(t *testing.T, ec *errcode.Error)
		// wantErrStr is a fallback string check for errors that pass through from
		// pathsafe (not directly errcode.New) — kept for symlink/containment cases.
		wantErrStr string
	}{
		{
			name: "missing CellID",
			spec: ScaffoldSpec{
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetail(ec, "field", "CellID") {
					t.Errorf("expected detail field=CellID, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "missing StructName",
			spec: ScaffoldSpec{
				CellID:     "foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetail(ec, "field", "StructName") {
					t.Errorf("expected detail field=StructName, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "missing Package",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetail(ec, "field", "Package") {
					t.Errorf("expected detail field=Package, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "missing ModulePath",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !strings.Contains(ec.Message, "ModulePath") {
					t.Errorf("expected message to contain 'ModulePath', got: %s", ec.Message)
				}
			},
		},
		{
			name: "missing OwnerTeam",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !strings.Contains(ec.Message, "OwnerTeam") {
					t.Errorf("expected message to contain 'OwnerTeam', got: %s", ec.Message)
				}
			},
		},
		{
			name: "missing OwnerRole",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !strings.Contains(ec.Message, "OwnerRole") {
					t.Errorf("expected message to contain 'OwnerRole', got: %s", ec.Message)
				}
			},
		},
		{
			name: "OwnerTeam with newline (YAML injection)",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform\ninjected: true",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetailKey(ec, "ownerTeam") {
					t.Errorf("expected detail key ownerTeam, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "OwnerTeam with colon-space (YAML injection)",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform: injected",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetailKey(ec, "ownerTeam") {
					t.Errorf("expected detail key ownerTeam, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "OwnerTeam with braces (YAML flow mapping injection)",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "{injected: true}",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetailKey(ec, "ownerTeam") {
					t.Errorf("expected detail key ownerTeam, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "OwnerTeam with path traversal",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "../etc/passwd",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetailKey(ec, "ownerTeam") {
					t.Errorf("expected detail key ownerTeam, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "OwnerRole with YAML injection",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner\ninjected: true",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !hasDetailKey(ec, "ownerRole") {
					t.Errorf("expected detail key ownerRole, got details: %v", ec.Details)
				}
			},
		},
		{
			name: "invalid ModulePath with backslash",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: `github.com\example\app`,
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !strings.Contains(ec.Message, "ModulePath") {
					t.Errorf("expected message to contain 'ModulePath', got: %s", ec.Message)
				}
			},
		},
		{
			name: "invalid ModulePath with dotdot",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/../evil",
				OwnerTeam:  "platform",
				OwnerRole:  "cell-owner",
			},
			wantCode: errcode.ErrValidationFailed,
			checkErr: func(t *testing.T, ec *errcode.Error) {
				t.Helper()
				if !strings.Contains(ec.Message, "ModulePath") {
					t.Errorf("expected message to contain 'ModulePath', got: %s", ec.Message)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			err := ScaffoldCell(dir, "cells/foo", tc.spec)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != tc.wantCode {
				t.Errorf("error code = %q, want %q", ec.Code, tc.wantCode)
			}
			if tc.checkErr != nil {
				tc.checkErr(t, ec)
			}
		})
	}
}

// detailAttrValue is a helper for accessing slog.Attr values in tests.
// Unused in production; used by TestScaffoldCell_ValidationErrors closures.
var _ = slog.String
