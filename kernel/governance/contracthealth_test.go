package governance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestCheckContractHealth contains the 14 unit cases migrated from
// cmd/gocell/app/check_test.go. They exercise CheckContractHealth logic
// without file I/O; Line/Column are not asserted here because the contracts
// are constructed in-memory without YAML file nodes.
func TestCheckContractHealth(t *testing.T) {
	tests := []struct {
		name      string
		contracts []*metadata.ContractMeta
		wantErr   bool
		wantMsg   string
	}{
		{
			name:      "empty list is healthy",
			contracts: nil,
			wantErr:   false,
		},
		{
			name: "all valid contracts pass",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.order.create.v1",
					Kind:      "http",
					OwnerCell: "ordercell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Request:  "request.schema.json",
						Response: "response.schema.json",
					},
				},
				{
					ID:        "event.order-created.v1",
					Kind:      "event",
					OwnerCell: "ordercell",
					Lifecycle: "active",
				},
			},
			wantErr: false,
		},
		{
			name: "missing lifecycle fails",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "",
				},
			},
			wantErr: true,
			wantMsg: "lifecycle",
		},
		{
			name: "missing ownerCell fails",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "",
					Lifecycle: "active",
				},
			},
			wantErr: true,
			wantMsg: "ownerCell",
		},
		{
			name: "http contract missing schemaRefs fails",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
				},
			},
			wantErr: true,
			wantMsg: "schemaRefs",
		},
		{
			name: "http contract with empty response schema fails",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Request: "request.schema.json",
					},
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							NoContent: false,
						},
					},
				},
			},
			wantErr: true,
			wantMsg: "response",
		},
		{
			name: "PUT contract missing request schema fails",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Response: "response.schema.json",
					},
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							Method: "PUT",
						},
					},
				},
			},
			wantErr: true,
			wantMsg: "request",
		},
		{
			name: "PATCH contract missing request schema fails",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Response: "response.schema.json",
					},
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							Method: "PATCH",
						},
					},
				},
			},
			wantErr: true,
			wantMsg: "request",
		},
		{
			name: "GET contract without request schema passes",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Response: "response.schema.json",
					},
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							Method: "GET",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "multiple issues reported simultaneously",
			contracts: []*metadata.ContractMeta{
				{
					ID:   "http.bad.v1",
					Kind: "http",
				},
			},
			wantErr: true,
			wantMsg: "ownerCell",
		},
		{
			name: "http noContent contract without response schema passes",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Request: "request.schema.json",
					},
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							NoContent: true,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "DELETE noContent with no schemas passes",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.delete.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							Method:    "DELETE",
							NoContent: true,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "responses entry missing schemaRef fails",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Response: "response.schema.json",
					},
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							Method: "POST",
							Responses: map[int]metadata.HTTPResponseMeta{
								401: {Description: "unauthorized", SchemaRef: ""},
							},
						},
					},
				},
			},
			wantErr: true,
			wantMsg: "schemaRef",
		},
		{
			name: "responses entry with schemaRef passes",
			contracts: []*metadata.ContractMeta{
				{
					ID:        "http.test.v1",
					Kind:      "http",
					OwnerCell: "test-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Response: "response.schema.json",
					},
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							Method: "POST",
							Responses: map[int]metadata.HTTPResponseMeta{
								401: {Description: "unauthorized", SchemaRef: "error.schema.json"},
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewValidator(&metadata.ProjectMeta{
				Contracts: map[string]*metadata.ContractMeta{},
			}, "", clock.Real())
			issues := v.CheckContractHealth(tt.contracts)
			assertContractHealthIssues(t, issues, tt.wantErr, tt.wantMsg)
		})
	}
}

// TestCheckContractHealth_PopulatesLineColumn is the key regression test for
// PR-A41 Task 3: locator must be consulted so Line/Column are non-zero when
// the contract.yaml has been parsed with file nodes. Previously
// contractHealthResult built findings via struct literal, bypassing the
// locator entirely — this test would have failed before the fix.
func TestCheckContractHealth_PopulatesLineColumn(t *testing.T) {
	// Write a contract.yaml that's missing ownerCell, parse it with
	// metadata.NewParser, then verify the CH-01 finding carries non-zero
	// Line/Column from the locator.
	dir := t.TempDir()
	// The parser requires contracts/{kind}/{domain...}/{version}/contract.yaml
	// with at least 5 path components (parts[0]=="contracts", parts[len-1]=="contract.yaml",
	// and len>=5). Use contracts/http/demo/v1/ to satisfy that invariant.
	contractDir := filepath.Join(dir, "contracts", "http", "demo", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	// ownerCell is present in the YAML but empty — the key exists in the node
	// tree so the locator can resolve its position (non-zero Line/Column).
	// This is the regression case: struct-literal findings (old code) produced
	// (0, 0) because they bypassed the locator entirely.
	contractYAML := `id: http.demo.v1
kind: http
ownerCell:
lifecycle: active
endpoints:
  http:
    method: GET
    path: /demo
schemaRefs:
  response: response.json
`
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "contract.yaml"), []byte(contractYAML), 0o644))

	parser := metadata.NewParser(dir)
	project, err := parser.Parse()
	require.NoError(t, err)

	contracts := make([]*metadata.ContractMeta, 0, len(project.Contracts))
	for _, c := range project.Contracts {
		contracts = append(contracts, c)
	}
	require.NotEmpty(t, contracts, "parser must find the contract.yaml")

	v := NewValidator(project, dir, clock.Real())
	results := v.CheckContractHealth(contracts)
	require.NotEmpty(t, results, "empty ownerCell must produce findings")

	var ownerFinding *ValidationResult
	for i := range results {
		if results[i].Code == codeCH01 {
			ownerFinding = &results[i]
			break
		}
	}
	require.NotNil(t, ownerFinding, "CH-01 ownerCell finding must be present")
	assert.Greater(t, ownerFinding.Line, 0, "Line must be populated by locator (regression: PR-A41)")
	assert.Greater(t, ownerFinding.Column, 0, "Column must be populated by locator (regression: PR-A41)")
}

// TestCheckContractHealth_CH03_PopulatesLineColumn covers the CH-03 case
// where a declared responses[N] entry is missing schemaRef. The leaf
// field doesn't exist in the YAML (that's why the rule fires), so the
// locator must walk up to the parent responses[N] block via the
// parentFieldPath fallback. Without that, IDE click-to-open and SARIF
// anchors degrade to file-only precision — the exact failure mode
// flagged in the round-3 review.
func TestCheckContractHealth_CH03_PopulatesLineColumn(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "demo", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	contractYAML := `id: http.demo.v1
kind: http
ownerCell: democell
lifecycle: active
endpoints:
  http:
    method: GET
    path: /demo
    responses:
      200:
        schemaRef: ok.json
      401:
        description: unauthorized
schemaRefs:
  response: ok.json
`
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "contract.yaml"), []byte(contractYAML), 0o644))

	parser := metadata.NewParser(dir)
	project, err := parser.Parse()
	require.NoError(t, err)

	contracts := make([]*metadata.ContractMeta, 0, len(project.Contracts))
	for _, c := range project.Contracts {
		contracts = append(contracts, c)
	}

	v := NewValidator(project, dir, clock.Real())
	results := v.CheckContractHealth(contracts)
	require.NotEmpty(t, results, "responses[401] missing schemaRef must produce a CH-03 finding")

	var schemaFinding *ValidationResult
	for i := range results {
		if results[i].Code == codeCH03 && strings.Contains(results[i].Field, "responses[401]") {
			schemaFinding = &results[i]
			break
		}
	}
	require.NotNil(t, schemaFinding, "CH-03 finding for responses[401] must be present")
	assert.Equal(t, "endpoints.http.responses[401].schemaRef", schemaFinding.Field,
		"display field path must keep the missing leaf for clarity")
	assert.Greater(t, schemaFinding.Line, 0,
		"Line must point at the parent responses[401] block (regression: round-3 P1)")
	assert.Greater(t, schemaFinding.Column, 0)
}

// assertContractHealthIssues centralizes the per-case assertion logic.
// Two-arm shape:
//   - wantErr=false: issues must be empty.
//   - wantErr=true:  at least one issue must mention wantMsg in its
//     message or field path; every issue must be SeverityError and
//     carry a CH-* code so JSON/SARIF consumers can route on it.
func assertContractHealthIssues(t *testing.T, issues []ValidationResult, wantErr bool, wantMsg string) {
	t.Helper()
	if !wantErr {
		assert.Empty(t, issues, "expected no validation issues")
		return
	}
	require.NotEmpty(t, issues, "expected validation issues")
	assert.True(t, hasContractHealthIssueMatching(issues, wantMsg),
		"expected issue containing %q, got: %v", wantMsg, issues)
	for _, issue := range issues {
		assert.Equal(t, SeverityError, issue.Severity,
			"contract-health findings must be SeverityError")
		assert.Contains(t, []RuleCode{codeCH01, codeCH02, codeCH03}, issue.Code,
			"contract-health code must be CH-* family")
	}
}

// hasContractHealthIssueMatching reports whether any finding mentions needle
// in its message or field path.
func hasContractHealthIssueMatching(issues []ValidationResult, needle string) bool {
	for _, issue := range issues {
		if strings.Contains(issue.Message, needle) ||
			strings.Contains(issue.Field, needle) {
			return true
		}
	}
	return false
}
