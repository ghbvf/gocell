package cellgen

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/testutil/fileutil"
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

// TestBuildGrpcServiceSpecFromCU_MethodPermissions verifies the per-method
// permission overlay (#2008) is composed into GrpcServiceGenSpec.MethodPermissions
// as (full method → action) pairs, sorted by full method name for deterministic
// golden output. Only permission entries contribute; a public entry does not.
func TestBuildGrpcServiceSpecFromCU_MethodPermissions(t *testing.T) {
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
				// WatchCommands sorts before IssueCommand by full method name; the
				// builder must sort, so the output order is deterministic regardless
				// of overlay declaration order. A public entry contributes nothing here.
				Methods: []metadata.GRPCMethodMeta{
					{Name: "IssueCommand", Permission: "device:command"},
					{Name: "WatchCommands", Permission: "device:command"},
					{Name: "PingPublic", Public: true},
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
	want := []MethodPermission{
		{FullMethod: "/device.command.v1.DeviceCommandService/IssueCommand", Permission: "device:command"},
		{FullMethod: "/device.command.v1.DeviceCommandService/WatchCommands", Permission: "device:command"},
	}
	if !slices.Equal(got.MethodPermissions, want) {
		t.Errorf("MethodPermissions = %+v, want %+v (sorted by full method; public entry excluded)", got.MethodPermissions, want)
	}
}

// TestBuildGrpcServiceSpecFromCU_PasswordResetExemptMethods verifies the per-method
// password-reset-exempt overlay (#1382) is composed into
// GrpcServiceGenSpec.PasswordResetExemptMethods as full method names
// (/{service}/{name}), only for passwordResetExempt:true entries. Entries without
// passwordResetExempt:true contribute nothing (fail-closed default).
func TestBuildGrpcServiceSpecFromCU_PasswordResetExemptMethods(t *testing.T) {
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
				// Mixed overlay: only the passwordResetExempt:true entry contributes;
				// the plain permission entry is excluded from PasswordResetExemptMethods.
				Methods: []metadata.GRPCMethodMeta{
					{Name: "IssueCommand", Permission: "device:command", PasswordResetExempt: true},
					{Name: "WatchCommands", Permission: "device:command"},
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
	if !slices.Equal(got.PasswordResetExemptMethods, want) {
		t.Errorf("PasswordResetExemptMethods = %v, want %v (non-exempt entry must be excluded)", got.PasswordResetExemptMethods, want)
	}
}

// TestRenderCell_GRPC_PasswordResetExempt verifies the template renders the
// PasswordResetExemptMethods field when present, and omits it when absent — the
// same guard as the PublicMethods field (empty guard → nothing rendered).
func TestRenderCell_GRPC_PasswordResetExempt(t *testing.T) {
	t.Parallel()
	root := synthGRPCRoot(t)

	// passwordResetExempt:true overlay with an accompanying permission (required by
	// schema: exempt methods are non-public and still need ABAC authorization).
	pm := buildGRPCProject()
	pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = []metadata.GRPCMethodMeta{
		{Name: "IssueCommand", Permission: "device:command", PasswordResetExempt: true},
	}

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

	if !bytes.Contains(out, []byte("PasswordResetExemptMethods")) {
		t.Errorf("grpc spec with passwordResetExempt:true must render PasswordResetExemptMethods field, got:\n%s", out)
	}
	if !bytes.Contains(out, []byte("/device.command.v1.DeviceCommandService/IssueCommand")) {
		t.Errorf("PasswordResetExemptMethods must contain the full method name, got:\n%s", out)
	}
}

// TestRenderCell_GRPC_PasswordResetExempt_Omitted verifies the template omits
// PasswordResetExemptMethods when no method in the overlay has passwordResetExempt:true.
func TestRenderCell_GRPC_PasswordResetExempt_Omitted(t *testing.T) {
	t.Parallel()
	root := synthGRPCRoot(t)

	// Permission-only overlay — no passwordResetExempt:true entry.
	pm := buildGRPCProject()
	pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = []metadata.GRPCMethodMeta{
		{Name: "IssueCommand", Permission: "device:command"},
	}

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

	if bytes.Contains(out, []byte("PasswordResetExemptMethods")) {
		t.Errorf("permission-only grpc cell must omit PasswordResetExemptMethods field, got:\n%s", out)
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

// TestRenderCell_GRPC_PermissionOnly_OmitsPublicMethods covers the template's
// {{- if .PublicMethods }} FALSE arm: a grpc contract whose overlay carries only
// permission entries (no public:true) must render a GRPCServiceSpec WITHOUT a
// PublicMethods field, but WITH a MethodPermissions map (#2008). Guards against a
// regression where the template emits an empty PublicMethods slice. (A no-overlay
// grpc contract is no longer renderable under #2008 strict fail-closed — the
// completeness pre-pass rejects an authed RPC with no permission; that path is
// covered by TestEnrichGrpcServices_IncompleteOverlayRejected.)
func TestRenderCell_GRPC_PermissionOnly_OmitsPublicMethods(t *testing.T) {
	t.Parallel()
	root := synthGRPCRoot(t)

	// Permission-only overlay (the synth proto exposes only IssueCommand): no
	// public entry → PublicMethods omitted; permission entry → MethodPermissions present.
	pm := buildGRPCProject()
	pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = []metadata.GRPCMethodMeta{
		{Name: "IssueCommand", Permission: "device:command"},
	}

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
		t.Errorf("permission-only grpc cell must omit the PublicMethods field, got:\n%s", out)
	}
	if !bytes.Contains(out, []byte("MethodPermissions")) {
		t.Errorf("permission-only grpc cell must render the MethodPermissions field, got:\n%s", out)
	}
	// Regression: guard against an empty-map render where the field name is present
	// but the mapping content is missing. Verify both the full method name and the
	// permission action string appear in the rendered output.
	if !bytes.Contains(out, []byte("/device.command.v1.DeviceCommandService/IssueCommand")) {
		t.Errorf("MethodPermissions must contain the full method name, got:\n%s", out)
	}
	if !bytes.Contains(out, []byte("device:command")) {
		t.Errorf("MethodPermissions must contain the permission action string, got:\n%s", out)
	}
}

// TestEnrichGrpcServices_IncompleteOverlayRejected proves the #2008 completeness
// pre-pass: a non-public proto RPC with no permission overlay entry is rejected at
// codegen (it would be a silently-dead 403 method at runtime — strict fail-closed).
// The synth proto exposes IssueCommand; an empty overlay leaves it uncovered.
func TestEnrichGrpcServices_IncompleteOverlayRejected(t *testing.T) {
	t.Parallel()
	root := synthGRPCRoot(t)

	pm := buildGRPCProject()
	pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = nil // no overlay → IssueCommand uncovered

	spec, err := BuildCellSpec(pm, "demo", markergen.WireBundle{}, idxOf(map[string]string{"command": "commandServer"}))
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	err = EnrichGrpcServicesWithProtoInfo(spec, root)
	if err == nil {
		t.Fatal("EnrichGrpcServicesWithProtoInfo must reject an incomplete overlay (uncovered authed RPC)")
	}
	if !strings.Contains(err.Error(), "no endpoints.grpc.methods entry") {
		t.Errorf("error must name the completeness violation, got: %v", err)
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

// synthGRPCMultiRoot returns the absolute path to the synth_grpc_multi testdata
// tree. The proto file under this root is:
//
//	contracts/grpc/device/command/v1/device_command_multi.proto
//
// It exposes three RPCs: IssueCommand, WatchCommands, CancelCommand — the
// minimum set required to exercise "partial missing" (forget one of N methods).
func synthGRPCMultiRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "synth_grpc_multi"))
	if err != nil {
		t.Fatalf("synthGRPCMultiRoot: %v", err)
	}
	return abs
}

// buildGRPCProjectMulti builds a ProjectMeta pointing at the three-RPC
// synth_grpc_multi proto. Methods overlay is left empty so each test case can
// set it independently.
func buildGRPCProjectMulti() *metadata.ProjectMeta {
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
				// Points at the three-RPC proto (IssueCommand, WatchCommands, CancelCommand).
				Proto:   "contracts/grpc/device/command/v1/device_command_multi.proto",
				Methods: nil, // set per test case
			},
		},
	}
	return fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
}

// TestEnrichGrpcServices_CompletenessGate_MultiRPC is the primary regression for
// F4 (review finding): the single-RPC synth proto cannot cover "partial missing"
// — a service with N RPCs where one is forgotten. This table-driven test uses the
// three-RPC synth_grpc_multi proto (IssueCommand, WatchCommands, CancelCommand)
// to exercise three of the four gate scenarios:
//
//   - Scenario 1 (full coverage): every RPC covered → PASS.
//   - Scenario 2 (partial missing): one of three RPCs has neither permission nor
//     public → FAIL with the completeness error.
//   - Scenario 3 (unknown method key): an overlay entry naming a method absent
//     from the proto → FAIL with the referential error.
//
// Scenario 4 (unknown permission value) is addressed by
// TestEnrichGrpcServices_UnknownPermission_PassesCellgenGate below — the cellgen
// completeness gate does NOT validate the permission string against the closed
// authz registry; that guard lives in governance FMT-41
// (kernel/governance.validateFMT41ForContract → authz.IsKnownPermissionString).
func TestEnrichGrpcServices_CompletenessGate_MultiRPC(t *testing.T) {
	t.Parallel()
	root := synthGRPCMultiRoot(t)

	cases := []struct {
		name    string
		methods []metadata.GRPCMethodMeta
		wantErr string // empty → expect success
	}{
		{
			// Scenario 1: all three RPCs covered — two with permission, one public.
			// EnrichGrpcServicesWithProtoInfo must accept this overlay and return nil.
			name: "scenario1_full_coverage_passes",
			methods: []metadata.GRPCMethodMeta{
				{Name: "IssueCommand", Permission: "device:command"},
				{Name: "WatchCommands", Permission: "device:command"},
				{Name: "CancelCommand", Public: true},
			},
			wantErr: "",
		},
		{
			// Scenario 2 (partial missing): WatchCommands has neither permission nor
			// public — it is uncovered. The completeness pre-pass must reject it with
			// the "no endpoints.grpc.methods entry" message identifying the uncovered
			// method. This is the core multi-RPC risk: single-RPC protos cannot
			// exercise this path because omitting the sole RPC looks like "no overlay"
			// rather than "partial overlay".
			name: "scenario2_partial_missing_rejected",
			methods: []metadata.GRPCMethodMeta{
				{Name: "IssueCommand", Permission: "device:command"},
				// WatchCommands deliberately omitted: partial coverage.
				{Name: "CancelCommand", Public: true},
			},
			wantErr: "no endpoints.grpc.methods entry",
		},
		{
			// Scenario 3 (unknown method key): overlay names "BogusMethod" which
			// does not exist in the proto service. The referential guard must reject
			// it — a stale overlay entry would be silently inert at runtime.
			name: "scenario3_unknown_method_key_rejected",
			methods: []metadata.GRPCMethodMeta{
				{Name: "IssueCommand", Permission: "device:command"},
				{Name: "WatchCommands", Permission: "device:command"},
				{Name: "BogusMethod", Permission: "device:command"}, // not an RPC of the proto
			},
			wantErr: "not an RPC of the proto service",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pm := buildGRPCProjectMulti()
			pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = tc.methods

			spec, err := BuildCellSpec(pm, "demo", markergen.WireBundle{}, idxOf(map[string]string{"command": "commandServer"}))
			if err != nil {
				t.Fatalf("BuildCellSpec: %v", err)
			}
			err = EnrichGrpcServicesWithProtoInfo(spec, root)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestEnrichGrpcServices_UnknownPermission_PassesCellgenGate documents that the
// cellgen completeness gate (validateGrpcMethodOverlayAgainstProto, called from
// EnrichGrpcServicesWithProtoInfo) does NOT validate whether a permission string
// is a member of the closed authz registry. A bogus permission string like
// "totally:bogus" passes cellgen successfully — it is syntactically non-empty,
// so the completeness check treats the RPC as covered. The closed-set guard lives
// in governance FMT-41 (kernel/governance.validateFMT41ForContract →
// authz.IsKnownPermissionString) which operates at `gocell validate` time on the
// YAML metadata, before codegen runs. The runtime registrar also re-checks via
// authz.PermissionByName (fail-fast at bootstrap). This test is a contract-of-absence:
// if this test FAILS (i.e. cellgen starts rejecting unknown permission strings),
// update the test AND this documentation to reflect the new behavior.
func TestEnrichGrpcServices_UnknownPermission_PassesCellgenGate(t *testing.T) {
	t.Parallel()
	root := synthGRPCMultiRoot(t)

	// All three RPCs covered; WatchCommands carries a permission string that is
	// NOT in the closed authz registry. Cellgen must accept this (completeness
	// satisfied) — governance FMT-41 and the runtime registrar are the actual guards.
	pm := buildGRPCProjectMulti()
	pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = []metadata.GRPCMethodMeta{
		{Name: "IssueCommand", Permission: "device:command"},
		{Name: "WatchCommands", Permission: "totally:bogus"}, // unknown permission string
		{Name: "CancelCommand", Public: true},
	}

	spec, err := BuildCellSpec(pm, "demo", markergen.WireBundle{}, idxOf(map[string]string{"command": "commandServer"}))
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	// Scenario 4: cellgen does NOT catch unknown permission strings.
	// If this assertion fires the cellgen gate has been strengthened — update the
	// test and the doc comment above.
	if err := EnrichGrpcServicesWithProtoInfo(spec, root); err != nil {
		t.Errorf("cellgen completeness gate must NOT reject unknown permission strings "+
			"(that is FMT-41's job); got unexpected error: %v", err)
	}
}

// TestEnrichGrpcServices_OwnerScopedCrossCheck exercises the #2207 Hard
// generate-time cross-check for owner-scoped permission ↔ resource selector
// consistency. This is an additional invariant on top of the existing completeness
// gate: an owner-scoped permission WITHOUT a resource selector, or a coarse
// permission WITH one, must be rejected at cellgen time.
//
// Cases:
//  1. Owner-scoped permission (device:consume) WITH resource → PASS.
//  2. Owner-scoped permission WITHOUT resource → FAIL (silent owner lock-out).
//  3. Coarse permission (device:command) WITH resource → FAIL (ignored, misconfiguration).
//  4. Coarse permission WITHOUT resource → PASS (existing behavior, unchanged).
func TestEnrichGrpcServices_OwnerScopedCrossCheck(t *testing.T) {
	t.Parallel()
	root := synthGRPCMultiRoot(t)

	cases := []struct {
		name    string
		methods []metadata.GRPCMethodMeta
		wantErr string // empty → expect success
	}{
		{
			name: "owner-scoped permission with resource → PASS",
			// device:consume is owner-scoped; it has a resource selector. The other
			// two RPCs use a coarse permission without a resource — both allowed.
			methods: []metadata.GRPCMethodMeta{
				// WatchCommands: owner-scoped, has resource.
				{Name: "WatchCommands", Permission: "device:consume", Resource: "device_id"},
				// IssueCommand: coarse, no resource.
				{Name: "IssueCommand", Permission: "device:command"},
				// CancelCommand: public (no permission gate).
				{Name: "CancelCommand", Public: true},
			},
			wantErr: "",
		},
		{
			name: "owner-scoped permission without resource → FAIL (owner lock-out)",
			// device:consume without resource: the PDP gate would use fullMethod as
			// resource; subject.sub == fullMethod never fires → owner silently locked out.
			methods: []metadata.GRPCMethodMeta{
				{Name: "WatchCommands", Permission: "device:consume"}, // no resource
				{Name: "IssueCommand", Permission: "device:command"},
				{Name: "CancelCommand", Public: true},
			},
			wantErr: "owner-scoped permission requires a resource selector",
		},
		{
			name: "coarse permission with resource → FAIL (ignored misconfiguration)",
			// device:command is coarse; a resource selector on it is ignored by the
			// interceptor — declaring it is a misconfiguration that cellgen must reject.
			methods: []metadata.GRPCMethodMeta{
				{Name: "IssueCommand", Permission: "device:command", Resource: "device_id"},
				{Name: "WatchCommands", Permission: "device:command"},
				{Name: "CancelCommand", Public: true},
			},
			wantErr: "resource selector on a coarse permission is ignored",
		},
		{
			name: "coarse permission without resource → PASS (unchanged behavior)",
			methods: []metadata.GRPCMethodMeta{
				{Name: "IssueCommand", Permission: "device:command"},
				{Name: "WatchCommands", Permission: "device:command"},
				{Name: "CancelCommand", Public: true},
			},
			wantErr: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pm := buildGRPCProjectMulti()
			pm.Contracts["grpc.device.command.v1"].Endpoints.GRPC.Methods = tc.methods

			spec, err := BuildCellSpec(pm, "demo", markergen.WireBundle{}, idxOf(map[string]string{"command": "commandServer"}))
			if err != nil {
				t.Fatalf("BuildCellSpec: %v", err)
			}
			err = EnrichGrpcServicesWithProtoInfo(spec, root)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				// PASS case (owner-scoped + resource): assert MethodResources contains
				// the WatchCommands→device_id mapping (#2207 cross-check).
				if tc.name == "owner-scoped permission with resource → PASS" {
					if len(spec.GrpcServices) == 0 {
						t.Fatal("PASS case must produce at least one GrpcService")
					}
					gs := spec.GrpcServices[0]
					watchFull := "/device.command.v1.DeviceCommandService/WatchCommands"
					var found bool
					for _, mr := range gs.MethodResources {
						if mr.FullMethod == watchFull && mr.Field == "device_id" {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("PASS case: MethodResources must contain %q→device_id, got %+v",
							watchFull, gs.MethodResources)
					}
					// Render the cell and assert the GENERATED code actually emits the
					// MethodResources mapping (#2349 F5): the spec-level field above can be
					// present while a template regression silently drops the rendered block,
					// breaking the registrar's resolver wiring at runtime. Lock the field
					// name + the full method name + the proto field value in the output.
					out, rerr := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
						TemplateName: "cell.tmpl",
						Templates:    templates,
						Data:         spec,
						Filename:     "demo/cell_gen.go",
					})
					if rerr != nil {
						t.Fatalf("Render: %v", rerr)
					}
					if !bytes.Contains(out, []byte("MethodResources")) {
						t.Errorf("owner-scoped grpc cell must render the MethodResources field, got:\n%s", out)
					}
					if !bytes.Contains(out, []byte(watchFull)) {
						t.Errorf("MethodResources must contain the full method name %q, got:\n%s", watchFull, out)
					}
					if !bytes.Contains(out, []byte(`"device_id"`)) {
						t.Errorf("MethodResources must contain the device_id field value, got:\n%s", out)
					}
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tc.wantErr)
				} else if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q must contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}
