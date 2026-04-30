package assembly

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	ecErr "github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildTestProject creates a ProjectMeta with a realistic multi-cell assembly
// suitable for testing boundary computation.
//
// Layout:
//   - assembly "ssobff" contains cells: accesscore, auditcore
//   - cell "configcore" is outside the assembly
//   - contract "http/auth/login" (http): server=accesscore, clients=[configcore]
//     → exported (provider inside, consumer outside)
//   - contract "event/session/created" (event): publisher=accesscore, subscribers=[auditcore]
//     → NOT exported (all consumers inside)
//   - contract "event/config/changed" (event): publisher=configcore, subscribers=[accesscore]
//     → imported (provider outside, consumer inside)
//   - contract "http/auth/me" (http): server=accesscore, clients=[]
//     → exported (provider inside, consumers empty)
func buildTestProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "identity", Role: "maintainer"},
				Schema:           metadata.SchemaMeta{Primary: "users"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.auth", "smoke.accesscore.session"}},
			},
			"auditcore": {
				ID:               "auditcore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "compliance", Role: "maintainer"},
				Schema:           metadata.SchemaMeta{Primary: "audit_logs"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.auditcore.audit"}},
			},
			"configcore": {
				ID:               "configcore",
				Type:             "support",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "maintainer"},
				Schema:           metadata.SchemaMeta{Primary: "config_entries"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.configcore.config"}},
			},
		},
		Slices: make(map[string]*metadata.SliceMeta),
		Contracts: map[string]*metadata.ContractMeta{
			"http/auth/login/v1": {
				ID:        "http/auth/login/v1",
				Kind:      "http",
				OwnerCell: "accesscore",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"configcore"},
				},
			},
			"event/session/created/v1": {
				ID:        "event/session/created/v1",
				Kind:      "event",
				OwnerCell: "accesscore",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   "accesscore",
					Subscribers: []string{"auditcore"},
				},
			},
			"event/config/changed/v1": {
				ID:        "event/config/changed/v1",
				Kind:      "event",
				OwnerCell: "configcore",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   "configcore",
					Subscribers: []string{"accesscore"},
				},
			},
			"http/auth/me/v1": {
				ID:        "http/auth/me/v1",
				Kind:      "http",
				OwnerCell: "accesscore",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{},
				},
			},
		},
		Journeys: make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"ssobff": {
				ID:    "ssobff",
				Cells: []string{"accesscore", "auditcore"},
				Build: metadata.BuildMeta{
					Entrypoint:     "cmd/ssobff/main.go",
					Binary:         "ssobff",
					DeployTemplate: "k8s/ssobff.yaml",
				},
			},
		},
		StatusBoard: nil,
		Actors:      nil,
	}
}

// ---------------------------------------------------------------------------
// GenerateEntrypoint tests
// ---------------------------------------------------------------------------

func TestGenerateEntrypoint_ContainsAssemblyID(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateEntrypoint("ssobff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "func run(ctx context.Context) error")
	assert.Contains(t, content, `runSsobff(ctx, "ssobff"`)
	assert.NotContains(t, content, "runCorebundle(ctx")
}

func TestGenerateEntrypoint_ContainsCellComments(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateEntrypoint("ssobff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, `"accesscore"`)
	assert.Contains(t, content, `"auditcore"`)
}

func TestGenerateEntrypoint_ContainsModulePath(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateEntrypoint("ssobff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, `"github.com/ghbvf/gocell/runtime/shutdown"`)
}

func TestGenerateEntrypoint_ContainsDoNotEdit(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateEntrypoint("ssobff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "DO NOT EDIT")
}

func TestGenerateEntrypoint_NotFoundAssembly(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateEntrypoint("nonexistent")
	require.Error(t, err)

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrAssemblyNotFound, ec.Code)
}

func TestGenerateEntrypoint_InvalidHelperNameReturnsMetadataError(t *testing.T) {
	project := buildTestProject()
	project.Assemblies["---"] = &metadata.AssemblyMeta{ID: "---"}
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateEntrypoint("---")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generated run helper")

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrMetadataInvalid, ec.Code)
}

func TestAssemblyRunHelperName_NormalizesSeparatorsUppercaseAndDigits(t *testing.T) {
	got, err := assemblyRunHelperName("sso-bff_2API")
	require.NoError(t, err)
	assert.Equal(t, "runSsoBff2API", got)

	_, err = assemblyRunHelperName("---")
	require.Error(t, err)
}

func TestExecuteTemplate_MissingTemplateReturnsMetadataError(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.executeTemplate("does-not-exist.tpl", nil)
	require.Error(t, err)

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrMetadataInvalid, ec.Code)
}

// ---------------------------------------------------------------------------
// GenerateBoundary tests
// ---------------------------------------------------------------------------

func TestGenerateBoundary_ExportedContracts(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)

	content := string(out)

	// http/auth/login/v1: provider=accesscore (inside), consumer=configcore (outside) → exported
	assert.Contains(t, content, "http/auth/login/v1")

	// http/auth/me/v1: provider=accesscore (inside), consumers=[] → exported
	assert.Contains(t, content, "http/auth/me/v1")
}

func TestGenerateBoundary_NotExportedWhenAllConsumersInside(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)

	content := string(out)

	// event/session/created/v1: provider=accesscore (inside), subscriber=auditcore (inside)
	// → NOT exported (all consumers inside)
	// It should NOT appear in the exportedContracts section.
	lines := strings.Split(content, "\n")
	inExported := false
	inImported := false
	for _, line := range lines {
		if strings.HasPrefix(line, "exportedContracts:") {
			inExported = true
			inImported = false
			continue
		}
		if strings.HasPrefix(line, "importedContracts:") {
			inExported = false
			inImported = true
			continue
		}
		if strings.HasPrefix(line, "smokeTargets:") {
			inExported = false
			inImported = false
			continue
		}
		if inExported {
			assert.NotContains(t, line, "event/session/created/v1",
				"event/session/created/v1 should NOT be exported (all consumers inside)")
		}
		_ = inImported // used for clarity
	}
}

func TestGenerateBoundary_ImportedContracts(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)

	content := string(out)

	// event/config/changed/v1: provider=configcore (outside), subscriber=accesscore (inside) → imported
	lines := strings.Split(content, "\n")
	inImported := false
	found := false
	for _, line := range lines {
		if strings.HasPrefix(line, "importedContracts:") {
			inImported = true
			continue
		}
		if strings.HasPrefix(line, "smokeTargets:") {
			inImported = false
			continue
		}
		if inImported && strings.Contains(line, "event/config/changed/v1") {
			found = true
		}
	}
	assert.True(t, found, "event/config/changed/v1 should be imported")
}

func TestGenerateBoundary_SmokeTargets(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)

	content := string(out)

	// accesscore smoke: smoke.accesscore.auth, smoke.accesscore.session
	// auditcore smoke: smoke.auditcore.audit
	assert.Contains(t, content, "smoke.accesscore.auth")
	assert.Contains(t, content, "smoke.accesscore.session")
	assert.Contains(t, content, "smoke.auditcore.audit")

	// configcore smoke should NOT appear (configcore is outside assembly)
	assert.NotContains(t, content, "smoke.configcore.config")
}

func TestGenerateBoundary_FingerprintNonEmpty(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "sourceFingerprint:")

	// Extract fingerprint value; it should be a 64-char hex string.
	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
		if after, ok := strings.CutPrefix(line, "sourceFingerprint:"); ok {
			fp := strings.TrimSpace(after)
			assert.Len(t, fp, 64, "SHA-256 hex digest should be 64 chars")
			break
		}
	}
}

func TestGenerateBoundary_ContainsAssemblyID(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "assemblyId: ssobff")
}

func TestGenerateBoundary_NotFoundAssembly(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateBoundary("nonexistent")
	require.Error(t, err)

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrAssemblyNotFound, ec.Code)
}

// ---------------------------------------------------------------------------
// sourceFingerprint with nil assembly (edge case)
// ---------------------------------------------------------------------------

// TestSourceFingerprint_NilAssembly verifies that sourceFingerprint returns ""
// when the assembly ID is not found in the project.
func TestSourceFingerprint_NilAssembly(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")
	fp, err := gen.sourceFingerprint("does-not-exist", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, fp, "sourceFingerprint must return empty string for unknown assembly")
}

// TestSourceFingerprint_MissingCellInAssembly verifies that sourceFingerprint
// handles assemblies that reference cells not in the project (missing cell path).
func TestSourceFingerprint_MissingCellInAssembly(t *testing.T) {
	project := buildTestProject()
	// Add an assembly that references a cell not in project.Cells.
	project.Assemblies["ghost-bundle"] = &metadata.AssemblyMeta{
		ID:    "ghost-bundle",
		Cells: []string{"ghost-cell"},
	}
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")
	fp, err := gen.sourceFingerprint("ghost-bundle", nil, nil)
	require.NoError(t, err)
	// Must return a non-empty fingerprint (missing cell falls back to "cell:<id>:missing").
	assert.NotEmpty(t, fp, "sourceFingerprint must return a fingerprint even with missing cells")
}

// ---------------------------------------------------------------------------
// Empty assembly tests
// ---------------------------------------------------------------------------

func TestGenerateBoundary_EmptyAssembly(t *testing.T) {
	project := buildTestProject()
	project.Assemblies["empty"] = &metadata.AssemblyMeta{
		ID:    "empty",
		Cells: []string{},
		Build: metadata.BuildMeta{},
	}
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("empty")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "assemblyId: empty")
	// Empty lists should render as []
	assert.Contains(t, content, "exportedContracts:\n  []")
	assert.Contains(t, content, "importedContracts:\n  []")
	assert.Contains(t, content, "smokeTargets:\n  []")
}

func TestGenerateEntrypoint_EmptyAssembly(t *testing.T) {
	project := buildTestProject()
	project.Assemblies["empty"] = &metadata.AssemblyMeta{
		ID:    "empty",
		Cells: []string{},
		Build: metadata.BuildMeta{},
	}
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateEntrypoint("empty")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, `runEmpty(ctx, "empty", []string{`)
	assert.NotContains(t, content, "accesscore")
}

// ---------------------------------------------------------------------------
// Fingerprint determinism test
// ---------------------------------------------------------------------------

func TestSourceFingerprint_Deterministic(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	cellSet := map[string]bool{"accesscore": true, "auditcore": true}
	exported, imported, err := gen.computeBoundaryContracts(cellSet)
	require.NoError(t, err)

	fp1, err := gen.sourceFingerprint("ssobff", exported, imported)
	require.NoError(t, err)
	fp2, err := gen.sourceFingerprint("ssobff", exported, imported)
	require.NoError(t, err)

	assert.Equal(t, fp1, fp2, "fingerprint should be deterministic")
	assert.Len(t, fp1, 64)
}

func TestSourceFingerprint_CellOrderIsStructural(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	cellSet := map[string]bool{"accesscore": true, "auditcore": true}
	exported, imported, err := gen.computeBoundaryContracts(cellSet)
	require.NoError(t, err)

	baseline, err := gen.sourceFingerprint("ssobff", exported, imported)
	require.NoError(t, err)

	project.Assemblies["ssobff"].Cells = []string{"auditcore", "accesscore"}
	got, err := gen.sourceFingerprint("ssobff", exported, imported)
	require.NoError(t, err)
	assert.NotEqual(t, baseline, got, "assembly cells order is runtime order and must change fingerprint")
}

func TestSourceFingerprint_NotFoundReturnsEmpty(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	fp, err := gen.sourceFingerprint("nonexistent", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, fp)
}

// ---------------------------------------------------------------------------
// Boundary with command/projection contract kinds
// ---------------------------------------------------------------------------

func TestGenerateBoundary_CommandAndProjectionKinds(t *testing.T) {
	project := buildTestProject()

	// Add a command contract: handler=auditcore (inside), invokers=[configcore] (outside)
	project.Contracts["command/audit/archive/v1"] = &metadata.ContractMeta{
		ID:        "command/audit/archive/v1",
		Kind:      "command",
		OwnerCell: "auditcore",
		Endpoints: metadata.EndpointsMeta{
			Handler:  "auditcore",
			Invokers: []string{"configcore"},
		},
	}

	// Add a projection contract: provider=configcore (outside), readers=[auditcore] (inside)
	project.Contracts["projection/config/snapshot/v1"] = &metadata.ContractMeta{
		ID:        "projection/config/snapshot/v1",
		Kind:      "projection",
		OwnerCell: "configcore",
		Endpoints: metadata.EndpointsMeta{
			Provider: "configcore",
			Readers:  []string{"auditcore"},
		},
	}

	// Rebuild generator to pick up new contracts
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)

	content := string(out)

	// command/audit/archive/v1: handler=auditcore (inside), invoker=configcore (outside) → exported
	assert.Contains(t, content, "command/audit/archive/v1")

	// projection/config/snapshot/v1: provider=configcore (outside), reader=auditcore (inside) → imported
	assert.Contains(t, content, "projection/config/snapshot/v1")
}

// ---------------------------------------------------------------------------
// Boundary error propagation on unknown contract kind
// ---------------------------------------------------------------------------

func TestGenerateBoundary_UnknownKindReturnsError(t *testing.T) {
	project := buildTestProject()
	project.Contracts["unknown.kind.v1"] = &metadata.ContractMeta{
		ID:        "unknown.kind.v1",
		Kind:      "grpc", // unknown kind
		OwnerCell: "accesscore",
		Endpoints: metadata.EndpointsMeta{Server: "accesscore"},
	}
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateBoundary("ssobff")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown.kind.v1")

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrValidationFailed, ec.Code)
}
