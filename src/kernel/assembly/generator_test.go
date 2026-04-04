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
//   - assembly "sso-bff" contains cells: access-core, audit-core
//   - cell "config-core" is outside the assembly
//   - contract "http/auth/login" (http): server=access-core, clients=[config-core]
//     → exported (provider inside, consumer outside)
//   - contract "event/session/created" (event): publisher=access-core, subscribers=[audit-core]
//     → NOT exported (all consumers inside)
//   - contract "event/config/changed" (event): publisher=config-core, subscribers=[access-core]
//     → imported (provider outside, consumer inside)
//   - contract "http/auth/me" (http): server=access-core, clients=[]
//     → exported (provider inside, consumers empty)
func buildTestProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "identity", Role: "maintainer"},
				Schema:           metadata.SchemaMeta{Primary: "users"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"test/smoke/auth_test.go", "test/smoke/session_test.go"}},
			},
			"audit-core": {
				ID:               "audit-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "compliance", Role: "maintainer"},
				Schema:           metadata.SchemaMeta{Primary: "audit_logs"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"test/smoke/audit_test.go"}},
			},
			"config-core": {
				ID:               "config-core",
				Type:             "support",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "maintainer"},
				Schema:           metadata.SchemaMeta{Primary: "config_entries"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"test/smoke/config_test.go"}},
			},
		},
		Slices:    make(map[string]*metadata.SliceMeta),
		Contracts: map[string]*metadata.ContractMeta{
			"http/auth/login/v1": {
				ID:        "http/auth/login/v1",
				Kind:      "http",
				OwnerCell: "access-core",
				Endpoints: metadata.EndpointsMeta{
					Server:  "access-core",
					Clients: []string{"config-core"},
				},
			},
			"event/session/created/v1": {
				ID:        "event/session/created/v1",
				Kind:      "event",
				OwnerCell: "access-core",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   "access-core",
					Subscribers: []string{"audit-core"},
				},
			},
			"event/config/changed/v1": {
				ID:        "event/config/changed/v1",
				Kind:      "event",
				OwnerCell: "config-core",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   "config-core",
					Subscribers: []string{"access-core"},
				},
			},
			"http/auth/me/v1": {
				ID:        "http/auth/me/v1",
				Kind:      "http",
				OwnerCell: "access-core",
				Endpoints: metadata.EndpointsMeta{
					Server:  "access-core",
					Clients: []string{},
				},
			},
		},
		Journeys: make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"sso-bff": {
				ID:    "sso-bff",
				Cells: []string{"access-core", "audit-core"},
				Build: metadata.BuildMeta{
					Entrypoint:     "cmd/sso-bff/main.go",
					Binary:         "sso-bff",
					DeployTemplate: "k8s/sso-bff.yaml",
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
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateEntrypoint("sso-bff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, `assembly.Config{ID: "sso-bff"}`)
}

func TestGenerateEntrypoint_ContainsCellComments(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateEntrypoint("sso-bff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "access-core")
	assert.Contains(t, content, "audit-core")
}

func TestGenerateEntrypoint_ContainsModulePath(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateEntrypoint("sso-bff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, `"github.com/ghbvf/gocell/kernel/assembly"`)
}

func TestGenerateEntrypoint_ContainsDoNotEdit(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateEntrypoint("sso-bff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "DO NOT EDIT")
}

func TestGenerateEntrypoint_NotFoundAssembly(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	_, err := gen.GenerateEntrypoint("nonexistent")
	require.Error(t, err)

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrAssemblyNotFound, ec.Code)
}

// ---------------------------------------------------------------------------
// GenerateBoundary tests
// ---------------------------------------------------------------------------

func TestGenerateBoundary_ExportedContracts(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateBoundary("sso-bff")
	require.NoError(t, err)

	content := string(out)

	// http/auth/login/v1: provider=access-core (inside), consumer=config-core (outside) → exported
	assert.Contains(t, content, "http/auth/login/v1")

	// http/auth/me/v1: provider=access-core (inside), consumers=[] → exported
	assert.Contains(t, content, "http/auth/me/v1")
}

func TestGenerateBoundary_NotExportedWhenAllConsumersInside(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateBoundary("sso-bff")
	require.NoError(t, err)

	content := string(out)

	// event/session/created/v1: provider=access-core (inside), subscriber=audit-core (inside)
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
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateBoundary("sso-bff")
	require.NoError(t, err)

	content := string(out)

	// event/config/changed/v1: provider=config-core (outside), subscriber=access-core (inside) → imported
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
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateBoundary("sso-bff")
	require.NoError(t, err)

	content := string(out)

	// access-core smoke: test/smoke/auth_test.go, test/smoke/session_test.go
	// audit-core smoke: test/smoke/audit_test.go
	assert.Contains(t, content, "test/smoke/auth_test.go")
	assert.Contains(t, content, "test/smoke/session_test.go")
	assert.Contains(t, content, "test/smoke/audit_test.go")

	// config-core smoke should NOT appear (config-core is outside assembly)
	assert.NotContains(t, content, "test/smoke/config_test.go")
}

func TestGenerateBoundary_FingerprintNonEmpty(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateBoundary("sso-bff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "sourceFingerprint:")

	// Extract fingerprint value; it should be a 64-char hex string.
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "sourceFingerprint:") {
			fp := strings.TrimSpace(strings.TrimPrefix(line, "sourceFingerprint:"))
			assert.Len(t, fp, 64, "SHA-256 hex digest should be 64 chars")
			break
		}
	}
}

func TestGenerateBoundary_ContainsAssemblyID(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateBoundary("sso-bff")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "assemblyId: sso-bff")
}

func TestGenerateBoundary_NotFoundAssembly(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	_, err := gen.GenerateBoundary("nonexistent")
	require.Error(t, err)

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ecErr.ErrAssemblyNotFound, ec.Code)
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
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

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
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateEntrypoint("empty")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, `assembly.Config{ID: "empty"}`)
	// No cell comments expected
	assert.NotContains(t, content, "access-core")
}

// ---------------------------------------------------------------------------
// Fingerprint determinism test
// ---------------------------------------------------------------------------

func TestSourceFingerprint_Deterministic(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	fp1 := gen.sourceFingerprint("sso-bff")
	fp2 := gen.sourceFingerprint("sso-bff")

	assert.Equal(t, fp1, fp2, "fingerprint should be deterministic")
	assert.Len(t, fp1, 64)
}

func TestSourceFingerprint_NotFoundReturnsEmpty(t *testing.T) {
	project := buildTestProject()
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	fp := gen.sourceFingerprint("nonexistent")
	assert.Empty(t, fp)
}

// ---------------------------------------------------------------------------
// Boundary with command/projection contract kinds
// ---------------------------------------------------------------------------

func TestGenerateBoundary_CommandAndProjectionKinds(t *testing.T) {
	project := buildTestProject()

	// Add a command contract: handler=audit-core (inside), invokers=[config-core] (outside)
	project.Contracts["command/audit/archive/v1"] = &metadata.ContractMeta{
		ID:        "command/audit/archive/v1",
		Kind:      "command",
		OwnerCell: "audit-core",
		Endpoints: metadata.EndpointsMeta{
			Handler:  "audit-core",
			Invokers: []string{"config-core"},
		},
	}

	// Add a projection contract: provider=config-core (outside), readers=[audit-core] (inside)
	project.Contracts["projection/config/snapshot/v1"] = &metadata.ContractMeta{
		ID:        "projection/config/snapshot/v1",
		Kind:      "projection",
		OwnerCell: "config-core",
		Endpoints: metadata.EndpointsMeta{
			Provider: "config-core",
			Readers:  []string{"audit-core"},
		},
	}

	// Rebuild generator to pick up new contracts
	gen := NewGenerator(project, "github.com/ghbvf/gocell")

	out, err := gen.GenerateBoundary("sso-bff")
	require.NoError(t, err)

	content := string(out)

	// command/audit/archive/v1: handler=audit-core (inside), invoker=config-core (outside) → exported
	assert.Contains(t, content, "command/audit/archive/v1")

	// projection/config/snapshot/v1: provider=config-core (outside), reader=audit-core (inside) → imported
	assert.Contains(t, content, "projection/config/snapshot/v1")
}
