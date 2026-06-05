package cellgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// synthGRPCRoot returns the absolute path to the synth_grpc testdata tree.
// The proto file under this root is:
//
//	contracts/grpc/device/command/v1/device_command.proto
func synthGRPCRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "synth_grpc"))
	if err != nil {
		t.Fatalf("synthGRPCRoot: %v", err)
	}
	return abs
}

// buildGRPCProject returns a ProjectMeta with one cell (demo/Demo), one slice
// (command), and one grpc contract (grpc.device.command.v1).
func buildGRPCProject() *metadata.ProjectMeta {
	cell := &metadata.CellMeta{
		ID:           "demo",
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "command",
		BelongsToCell: "demo",
		Dir:           "command",
		File:          "cells/demo/slices/command/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "grpc.device.command.v1", Role: "serve"},
		},
	}
	contract := &metadata.ContractMeta{
		ID:   "grpc.device.command.v1",
		Kind: "grpc",
		Endpoints: metadata.EndpointsMeta{
			Server: "demo",
			GRPC: &metadata.GRPCTransportMeta{
				Service: "device.command.v1.DeviceCommandService",
				Method:  "IssueCommand",
				Proto:   "contracts/grpc/device/command/v1/device_command.proto",
			},
		},
	}
	return fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
}

// TestBuildGrpcServiceSpecFromCU exercises buildGrpcServiceSpecFromCU in
// isolation: table-driven tests cover the happy path, unknown contract, non-grpc
// kind, empty Service, empty Proto, and ambiguous-field scenarios.
func TestBuildGrpcServiceSpecFromCU(t *testing.T) {
	t.Parallel()

	cell := &metadata.CellMeta{
		ID:           "demo",
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	makeSlice := func(cu metadata.ContractUsage) *metadata.SliceMeta {
		return &metadata.SliceMeta{
			ID:             "command",
			BelongsToCell:  "demo",
			Dir:            "command",
			File:           "cells/demo/slices/command/slice.yaml",
			ContractUsages: []metadata.ContractUsage{cu},
		}
	}
	goodContract := &metadata.ContractMeta{
		ID:   "grpc.device.command.v1",
		Kind: "grpc",
		Endpoints: metadata.EndpointsMeta{
			Server: "demo",
			GRPC: &metadata.GRPCTransportMeta{
				Service: "device.command.v1.DeviceCommandService",
				Method:  "IssueCommand",
				Proto:   "contracts/grpc/device/command/v1/device_command.proto",
			},
		},
	}

	cases := []struct {
		name       string
		cu         metadata.ContractUsage
		contracts  map[string]*metadata.ContractMeta
		fieldIndex *CellFieldIndex
		wantErr    string
		wantSpec   *GrpcServiceGenSpec
	}{
		{
			name:       "happy path",
			cu:         metadata.ContractUsage{Contract: "grpc.device.command.v1", Role: "serve"},
			contracts:  map[string]*metadata.ContractMeta{"grpc.device.command.v1": goodContract},
			fieldIndex: idxOf(map[string]string{"command": "commandServer"}),
			wantSpec: &GrpcServiceGenSpec{
				ContractID:    "grpc.device.command.v1",
				SliceID:       "command",
				HandlerField:  "commandServer",
				RegisterFunc:  "RegisterDeviceCommandServiceServer",
				ListenerConst: "cell.PrimaryListener",
				ProtoRel:      "contracts/grpc/device/command/v1/device_command.proto",
				Service:       "device.command.v1.DeviceCommandService",
				Method:        "IssueCommand",
			},
		},
		{
			name:       "unknown contract",
			cu:         metadata.ContractUsage{Contract: "grpc.device.missing.v1", Role: "serve"},
			contracts:  map[string]*metadata.ContractMeta{},
			fieldIndex: idxOf(map[string]string{"command": "commandServer"}),
			wantErr:    "unknown contract",
		},
		{
			name: "non-grpc kind",
			cu:   metadata.ContractUsage{Contract: "event.foo.v1", Role: "serve"},
			contracts: map[string]*metadata.ContractMeta{
				"event.foo.v1": {ID: "event.foo.v1", Kind: "event"},
			},
			fieldIndex: idxOf(map[string]string{"command": "commandServer"}),
			wantErr:    "kind grpc",
		},
		{
			name: "empty Service",
			cu:   metadata.ContractUsage{Contract: "grpc.device.command.v1", Role: "serve"},
			contracts: map[string]*metadata.ContractMeta{
				"grpc.device.command.v1": {
					ID:   "grpc.device.command.v1",
					Kind: "grpc",
					Endpoints: metadata.EndpointsMeta{
						GRPC: &metadata.GRPCTransportMeta{
							Service: "",
							Method:  "IssueCommand",
							Proto:   "contracts/grpc/device/command/v1/device_command.proto",
						},
					},
				},
			},
			fieldIndex: idxOf(map[string]string{"command": "commandServer"}),
			wantErr:    "service",
		},
		{
			name: "empty Proto",
			cu:   metadata.ContractUsage{Contract: "grpc.device.command.v1", Role: "serve"},
			contracts: map[string]*metadata.ContractMeta{
				"grpc.device.command.v1": {
					ID:   "grpc.device.command.v1",
					Kind: "grpc",
					Endpoints: metadata.EndpointsMeta{
						GRPC: &metadata.GRPCTransportMeta{
							Service: "device.command.v1.DeviceCommandService",
							Method:  "IssueCommand",
							Proto:   "",
						},
					},
				},
			},
			fieldIndex: idxOf(map[string]string{"command": "commandServer"}),
			wantErr:    "proto",
		},
		{
			name: "nil GRPC block",
			cu:   metadata.ContractUsage{Contract: "grpc.device.command.v1", Role: "serve"},
			contracts: map[string]*metadata.ContractMeta{
				"grpc.device.command.v1": {
					ID:        "grpc.device.command.v1",
					Kind:      "grpc",
					Endpoints: metadata.EndpointsMeta{},
				},
			},
			fieldIndex: idxOf(map[string]string{"command": "commandServer"}),
			wantErr:    "grpc block",
		},
		{
			name:       "ambiguous field",
			cu:         metadata.ContractUsage{Contract: "grpc.device.command.v1", Role: "serve"},
			contracts:  map[string]*metadata.ContractMeta{"grpc.device.command.v1": goodContract},
			fieldIndex: idxOf(map[string]string{"command": ambiguousField}),
			wantErr:    "multiple",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			slc := makeSlice(tc.cu)
			p := fixtureProject(cell, []*metadata.SliceMeta{slc}, contractsSlice(tc.contracts))
			got, err := buildGrpcServiceSpecFromCU(p, "demo", "command", tc.cu, tc.fieldIndex)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantSpec != nil {
				if got.ContractID != tc.wantSpec.ContractID ||
					got.SliceID != tc.wantSpec.SliceID ||
					got.HandlerField != tc.wantSpec.HandlerField ||
					got.RegisterFunc != tc.wantSpec.RegisterFunc ||
					got.ListenerConst != tc.wantSpec.ListenerConst ||
					got.ProtoRel != tc.wantSpec.ProtoRel ||
					got.Service != tc.wantSpec.Service ||
					got.Method != tc.wantSpec.Method {
					t.Errorf("GrpcServiceGenSpec mismatch:\n got:  %+v\nwant: %+v", got, *tc.wantSpec)
				}
			}
		})
	}
}

// TestRenderCell_GoldenGRPC renders cell_gen.go for the synth_grpc project and
// compares against a committed golden file. Run with -update to regenerate.
func TestRenderCell_GoldenGRPC(t *testing.T) {
	t.Parallel()
	root := synthGRPCRoot(t)

	spec, err := BuildCellSpec(buildGRPCProject(), "demo", markergen.WireBundle{}, idxOf(map[string]string{"command": "commandServer"}))
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if err := EnrichGrpcServicesWithProtoInfo(spec, root); err != nil {
		t.Fatalf("EnrichGrpcServicesWithProtoInfo: %v", err)
	}

	out, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "cell.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     "demo/cell_gen.go",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden", "synth_grpc_cell_gen.go.golden")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, out, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden file updated: %s", goldenPath)
		return
	}

	golden := fileutil.MustReadFile(t, goldenPath)
	if !bytes.Equal(out, golden) {
		t.Errorf("rendered output diverges from golden:\n--- got ---\n%s\n--- want ---\n%s", out, golden)
	}
}

// TestRenderCell_GRPCImportsPresent verifies that cell.tmpl with a GrpcServices
// entry emits the grpc import + pb alias import + reg.GRPCService call without
// hitting the filesystem golden comparison (unit smoke test).
func TestRenderCell_GRPCImportsPresent(t *testing.T) {
	t.Parallel()
	spec := &CellGenSpec{
		Package:              "demo",
		StructName:           "Demo",
		CellID:               "demo",
		ConsumerGroupDefault: "demo",
		RenderedMetaLiteral:  "&metadata.CellMeta{}",
		GrpcServices: []GrpcServiceGenSpec{{
			ContractID:    "grpc.device.command.v1",
			SliceID:       "command",
			HandlerField:  "commandServer",
			RegisterFunc:  "RegisterDeviceCommandServiceServer",
			ListenerConst: "cell.PrimaryListener",
			ProtoRel:      "contracts/grpc/device/command/v1/device_command.proto",
			Service:       "device.command.v1.DeviceCommandService",
			Method:        "IssueCommand",
			PbImportPath:  "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1",
			PbAlias:       "grpc0",
		}},
	}

	out, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "cell.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     "demo/cell_gen.go",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(out)

	mustContain(t, got, `"google.golang.org/grpc"`)
	mustContain(t, got, `grpc0 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"`)
	mustContain(t, got, `reg.GRPCService(cell.GRPCServiceSpec{`)
	mustContain(t, got, `ContractID: "grpc.device.command.v1"`)
	mustContain(t, got, `CellID:     "demo"`)
	mustContain(t, got, `Listener:   cell.PrimaryListener`)
	mustContain(t, got, `grpc0.RegisterDeviceCommandServiceServer(r, c.commandServer)`)
	mustContain(t, got, `"fmt"`)
}

// contractsSlice converts a map to a slice for use with fixtureProject.
func contractsSlice(m map[string]*metadata.ContractMeta) []*metadata.ContractMeta {
	out := make([]*metadata.ContractMeta, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
