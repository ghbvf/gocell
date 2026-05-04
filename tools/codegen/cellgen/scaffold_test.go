package cellgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	}

	if err := ScaffoldCell(dir, "cells/barcell", spec); err != nil {
		t.Fatalf("ScaffoldCell() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "cells", "barcell", "cell.go")) //nolint:gosec // test reads files it just wrote
	if err != nil {
		t.Fatalf("read cell.go: %v", err)
	}

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
	}

	if err := ScaffoldCell(dir, "cells/bazcore", spec); err != nil {
		t.Fatalf("ScaffoldCell() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "cells", "bazcore", "cell.yaml")) //nolint:gosec // test reads files it just wrote
	if err != nil {
		t.Fatalf("read cell.yaml: %v", err)
	}

	if !strings.Contains(string(content), "goStructName:") {
		t.Error("cell.yaml missing goStructName field")
	}
	if !strings.Contains(string(content), "BazCore") {
		t.Error("cell.yaml goStructName does not contain StructName")
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
			},
		},
		{
			name: "cell with hyphenated-like naming avoided",
			spec: ScaffoldSpec{
				CellID:     "iotdevice",
				StructName: "IoTDevice",
				Package:    "iotdevice",
				ModulePath: "github.com/acme/iot",
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

			cellGo, err := os.ReadFile(filepath.Join(dir, targetDir, "cell.go")) //nolint:gosec // test reads files it just wrote
			if err != nil {
				t.Fatalf("read cell.go: %v", err)
			}
			cellYAML, err := os.ReadFile(filepath.Join(dir, targetDir, "cell.yaml")) //nolint:gosec // test reads files it just wrote
			if err != nil {
				t.Fatalf("read cell.yaml: %v", err)
			}

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
		name              string
		cellType          string
		level             string
		wantType          string
		wantLevel         string
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
				Type:             tc.cellType,
				ConsistencyLevel: tc.level,
			}
			if err := ScaffoldCell(dir, "cells/typecell", spec); err != nil {
				t.Fatalf("ScaffoldCell() error = %v", err)
			}
			content, err := os.ReadFile(filepath.Join(dir, "cells", "typecell", "cell.yaml")) //nolint:gosec // test reads files it just wrote
			if err != nil {
				t.Fatalf("read cell.yaml: %v", err)
			}
			if !strings.Contains(string(content), tc.wantType) {
				t.Errorf("cell.yaml missing %q, got:\n%s", tc.wantType, content)
			}
			if !strings.Contains(string(content), tc.wantLevel) {
				t.Errorf("cell.yaml missing %q, got:\n%s", tc.wantLevel, content)
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

// TestScaffoldCell_DryRun verifies that ScaffoldCell with DryRun=true performs
// conflict detection but does not write any files.
func TestScaffoldCell_DryRun(t *testing.T) {
	t.Run("no files written when no conflict", func(t *testing.T) {
		dir := t.TempDir()
		spec := ScaffoldSpec{
			CellID:     "drycell",
			StructName: "DryCell",
			Package:    "drycell",
			ModulePath: "github.com/example/app",
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
}

// TestScaffoldCell_ValidationErrors verifies that missing required fields
// are rejected before any filesystem operation.
func TestScaffoldCell_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		spec    ScaffoldSpec
		wantErr string
	}{
		{
			name: "missing CellID",
			spec: ScaffoldSpec{
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
			},
			wantErr: "CellID is required",
		},
		{
			name: "missing StructName",
			spec: ScaffoldSpec{
				CellID:     "foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
			},
			wantErr: "StructName is required",
		},
		{
			name: "missing Package",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				ModulePath: "github.com/example/app",
			},
			wantErr: "Package is required",
		},
		{
			name: "missing ModulePath",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
			},
			wantErr: "ModulePath is required",
		},
		{
			name: "OwnerTeam with newline (YAML injection)",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform\ninjected: true",
			},
			wantErr: "OwnerTeam",
		},
		{
			name: "OwnerTeam with colon-space (YAML injection)",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "platform: injected",
			},
			wantErr: "OwnerTeam",
		},
		{
			name: "OwnerTeam with braces (YAML flow mapping injection)",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "{injected: true}",
			},
			wantErr: "OwnerTeam",
		},
		{
			name: "OwnerTeam with path traversal",
			spec: ScaffoldSpec{
				CellID:     "foo",
				StructName: "Foo",
				Package:    "foo",
				ModulePath: "github.com/example/app",
				OwnerTeam:  "../etc/passwd",
			},
			wantErr: "OwnerTeam",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			err := ScaffoldCell(dir, "cells/foo", tc.spec)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}
