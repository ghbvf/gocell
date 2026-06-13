package cellgen

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
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
				Proto:   "contracts/grpc/device/command/v1/device_command.proto",
				// Per-method public overlay (#1675): IssueCommand is JWT-exempt, so
				// the golden must carry PublicMethods for the full method name.
				Methods: []metadata.GRPCMethodMeta{{Name: "IssueCommand", Public: true}},
			},
		},
	}
	return fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
}

// TestBuildGrpcServiceSpecFromCU_PublicMethods verifies the per-method public
// overlay (#1675) is composed into GrpcServiceGenSpec.PublicMethods as full
// method names (/{service}/{name}), only for public:true entries. A public:false
// (or absent) entry contributes nothing — keeping the fail-closed default.
func TestBuildGrpcServiceSpecFromCU_PublicMethods(t *testing.T) {
	t.Parallel()

	cell := &metadata.CellMeta{
		ID: "demo", Dir: "demo", File: "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	contract := &metadata.ContractMeta{
		ID: "grpc.device.command.v1", Kind: "grpc",
		Endpoints: metadata.EndpointsMeta{
			Server: "demo",
			GRPC: &metadata.GRPCTransportMeta{
				Service: "device.command.v1.DeviceCommandService",
				Proto:   "contracts/grpc/device/command/v1/device_command.proto",
				// Mixed overlay: only the public:true entry contributes; the
				// public:false entry is excluded (fail-closed default).
				Methods: []metadata.GRPCMethodMeta{
					{Name: "IssueCommand", Public: true},
					{Name: "WatchCommands", Public: false},
				},
			},
		},
	}
	cu := metadata.ContractUsage{Contract: "grpc.device.command.v1", Role: "serve"}
	slc := &metadata.SliceMeta{
		ID: "command", BelongsToCell: "demo", Dir: "command",
		File:           "cells/demo/slices/command/slice.yaml",
		ContractUsages: []metadata.ContractUsage{cu},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})

	got, err := buildGrpcServiceSpecFromCU(p, "demo", "command", cu, idxOf(map[string]string{"command": "commandServer"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"/device.command.v1.DeviceCommandService/IssueCommand"}
	if !slices.Equal(got.PublicMethods, want) {
		t.Errorf("PublicMethods = %v, want %v (public:false entry must be excluded)", got.PublicMethods, want)
	}
}

// TestEnrichGrpcServices_BogusOverlayMethodRejected proves the cellgen path
// (gocell generate cell) fail-closes a public-method overlay entry that names an
// RPC absent from the proto service — the sibling of contractgen's
// validateGRPCMethodOverlay, so generate-cell alone can't render an inert public
// entry (#1675 review F3). The synth proto exposes only IssueCommand.
func TestEnrichGrpcServices_BogusOverlayMethodRejected(t *testing.T) {
	t.Parallel()
	root := synthGRPCRoot(t)

	pm := buildGRPCProject()
	pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = []metadata.GRPCMethodMeta{
		{Name: "BogusRPC", Public: true},
	}
	spec, err := BuildCellSpec(pm, "demo", markergen.WireBundle{}, idxOf(map[string]string{"command": "commandServer"}))
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	err = EnrichGrpcServicesWithProtoInfo(spec, root)
	if err == nil || !strings.Contains(err.Error(), "not an RPC of the proto service") {
		t.Fatalf("expected referential rejection of bogus overlay method, got %v", err)
	}
}

// TestRenderCell_GRPC_NoOverlay_OmitsPublicMethods covers the template's
// {{- if .PublicMethods }} FALSE arm: a grpc contract with no methods overlay must
// render a GRPCServiceSpec WITHOUT a PublicMethods field (fail-closed default).
// Guards against a regression where the template emits an empty PublicMethods slice.
func TestRenderCell_GRPC_NoOverlay_OmitsPublicMethods(t *testing.T) {
	t.Parallel()
	root := synthGRPCRoot(t)

	// buildGRPCProject() declares an overlay; strip it for the no-overlay arm.
	pm := buildGRPCProject()
	pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = nil

	spec, err := BuildCellSpec(pm, "demo", markergen.WireBundle{}, idxOf(map[string]string{"command": "commandServer"}))
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
	if bytes.Contains(out, []byte("PublicMethods")) {
		t.Errorf("no-overlay grpc cell must omit the PublicMethods field, got:\n%s", out)
	}
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
					got.Service != tc.wantSpec.Service {
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

// TestBuildGrpcServicesFromSlices_SkipAndError exercises the skip predicate
// of buildGrpcServicesFromSlices:
//   - a role:serve CU on a kind:http contract is silently skipped (HTTP route,
//     handled by markergen), producing no spec and no error.
//   - a role:serve CU on an unknown contract id is NOT skipped and returns an
//     explicit "unknown contract" error (mirrors the subscribe-path behavior).
func TestBuildGrpcServicesFromSlices_SkipAndError(t *testing.T) {
	t.Parallel()

	cell := &metadata.CellMeta{
		ID:           "demo",
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}

	t.Run("http contract is silently skipped", func(t *testing.T) {
		t.Parallel()
		slc := &metadata.SliceMeta{
			ID:            "webapi",
			BelongsToCell: "demo",
			Dir:           "webapi",
			File:          "cells/demo/slices/webapi/slice.yaml",
			ContractUsages: []metadata.ContractUsage{
				{Contract: "http.device.v1", Role: "serve"},
			},
		}
		httpContract := &metadata.ContractMeta{ID: "http.device.v1", Kind: "http"}
		p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{httpContract})
		idx := idxOf(map[string]string{"webapi": "webSvc"})
		specs, err := buildGrpcServicesFromSlices(p, "demo", idx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(specs) != 0 {
			t.Fatalf("expected 0 specs for http contract, got %d: %+v", len(specs), specs)
		}
	})

	t.Run("unknown contract returns error", func(t *testing.T) {
		t.Parallel()
		slc := &metadata.SliceMeta{
			ID:            "command",
			BelongsToCell: "demo",
			Dir:           "command",
			File:          "cells/demo/slices/command/slice.yaml",
			ContractUsages: []metadata.ContractUsage{
				{Contract: "grpc.typo.v1", Role: "serve"},
			},
		}
		p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{})
		idx := idxOf(map[string]string{"command": "commandServer"})
		_, err := buildGrpcServicesFromSlices(p, "demo", idx)
		if err == nil {
			t.Fatal("expected error for unknown contract, got nil")
		}
		if !strings.Contains(err.Error(), "unknown contract") {
			t.Fatalf("error %q does not contain %q", err, "unknown contract")
		}
	})
}

// TestValidateGrpcContractEndpoint_Guards exercises the individual guards added
// to validateGrpcContractEndpoint: traversal path rejection and empty service.
// The method field was removed in #1655 (service-level granularity); the proto
// file is now the single source of truth for the method set.
func TestValidateGrpcContractEndpoint_Guards(t *testing.T) {
	t.Parallel()

	baseGRPC := func(service, proto string) *metadata.ContractMeta {
		return &metadata.ContractMeta{
			ID:   "grpc.device.command.v1",
			Kind: "grpc",
			Endpoints: metadata.EndpointsMeta{
				GRPC: &metadata.GRPCTransportMeta{
					Service: service,
					Proto:   proto,
				},
			},
		}
	}

	cases := []struct {
		name     string
		contract *metadata.ContractMeta
		wantErr  string
	}{
		{
			name:     "traversal path rejected",
			contract: baseGRPC("device.command.v1.DeviceCommandService", "contracts/grpc/../../../etc/x"),
			wantErr:  "local path",
		},
		{
			name:     "path outside contracts/grpc/ rejected",
			contract: baseGRPC("device.command.v1.DeviceCommandService", "etc/passwd"),
			wantErr:  "rooted under",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &metadata.ProjectMeta{
				Contracts: map[string]*metadata.ContractMeta{
					"grpc.device.command.v1": tc.contract,
				},
			}
			_, err := validateGrpcContractEndpoint(p, "demo", "command", "grpc.device.command.v1")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestEnrichGrpcServicesWithProtoInfo_ErrorPath verifies that when ProtoRel
// points at a non-existent file the returned error message contains both the
// contractID and sliceID for diagnostics.
func TestEnrichGrpcServicesWithProtoInfo_ErrorPath(t *testing.T) {
	t.Parallel()

	spec := &CellGenSpec{
		GrpcServices: []GrpcServiceGenSpec{
			{
				ContractID:   "grpc.device.command.v1",
				SliceID:      "command",
				HandlerField: "commandServer",
				RegisterFunc: "RegisterDeviceCommandServiceServer",
				ProtoRel:     "contracts/grpc/device/command/v1/does_not_exist.proto",
				Service:      "device.command.v1.DeviceCommandService",
			},
		},
	}

	err := EnrichGrpcServicesWithProtoInfo(spec, "/tmp/nonexistent_root_for_cellgen_test")
	if err == nil {
		t.Fatal("expected error for non-existent proto file, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "grpc.device.command.v1") {
		t.Errorf("error %q does not contain contractID %q", errStr, "grpc.device.command.v1")
	}
	if !strings.Contains(errStr, "command") {
		t.Errorf("error %q does not contain sliceID %q", errStr, "command")
	}
}

// TestGrpcLastSegment covers the FQN → simple-name extraction (pure segment
// extraction, no Go-identifier validation). Note: "a.b.c.d" correctly extracts
// "d" here; whether "d" is a valid exported Go identifier is validated separately
// by metadata.GRPCServiceGoName (see TestGRPCServiceGoName in kernel/metadata).
func TestGrpcLastSegment(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
	}{
		{"a.b.C", "C"},
		{"DeviceCommandService", "DeviceCommandService"},
		{"", ""},
		{"device.command.v1.DeviceCommandService", "DeviceCommandService"},
		// "d" is the correct last segment; metadata.GRPCServiceGoName rejects it
		// as unexported — grpcLastSegment itself is a pure extractor.
		{"a.b.c.d", "d"},
	}

	for _, tc := range cases {
		got := grpcLastSegment(tc.input)
		if got != tc.want {
			t.Errorf("grpcLastSegment(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// contractsSlice converts a map to a slice for use with fixtureProject.
func contractsSlice(m map[string]*metadata.ContractMeta) []*metadata.ContractMeta {
	out := make([]*metadata.ContractMeta, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
