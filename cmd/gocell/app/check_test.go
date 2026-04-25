package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateContractHealth(t *testing.T) {
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
			issues := validateContractHealth(tt.contracts)
			assertContractHealthIssues(t, issues, tt.wantErr, tt.wantMsg)
		})
	}
}

// assertContractHealthIssues centralises the per-case assertion logic so
// TestValidateContractHealth's body stays under the cognitive complexity
// budget. Two-arm shape:
//   - wantErr=false: issues must be empty.
//   - wantErr=true:  at least one issue must mention wantMsg in its
//     message or field path; every issue must be SeverityError and
//     carry a CH-* code so JSON/SARIF consumers can route on it.
func assertContractHealthIssues(t *testing.T, issues []governance.ValidationResult, wantErr bool, wantMsg string) {
	t.Helper()
	if !wantErr {
		assert.Empty(t, issues, "expected no validation issues")
		return
	}
	require.NotEmpty(t, issues, "expected validation issues")
	assert.True(t, hasIssueMatching(issues, wantMsg),
		"expected issue containing %q, got: %v", wantMsg, issues)
	for _, issue := range issues {
		assert.Equal(t, governance.SeverityError, issue.Severity,
			"contract-health findings must be SeverityError")
		assert.Contains(t, []string{"CH-01", "CH-02", "CH-03"}, issue.Code,
			"contract-health code must be CH-* family")
	}
}

// hasIssueMatching reports whether any finding mentions needle in its
// message or field path. Pulled out so the substring search isn't
// inlined into the assertion helper.
func hasIssueMatching(issues []governance.ValidationResult, needle string) bool {
	for _, issue := range issues {
		if strings.Contains(issue.Message, needle) ||
			strings.Contains(issue.Field, needle) {
			return true
		}
	}
	return false
}

func TestCheckContractHealthCI(t *testing.T) {
	// Run against the real project — should pass with 0 issues.
	err := runCheck([]string{"contract-health"})
	assert.NoError(t, err, "contract-health should pass on the project's contracts")
}

// TestCheckContractHealth_JSONFormat verifies --format=json emits a
// machine-readable document. We skip when running outside the gocell tree
// (the real project must be reachable for findRoot()), and we only check
// the structural shape — exact issue list depends on repo state.
func TestCheckContractHealth_JSONFormat(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"contract-health", "--format=json"})
	})
	require.NotEmpty(t, out, "JSON format must produce output")

	var doc struct {
		Issues  []map[string]any `json:"issues"`
		Summary struct {
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc),
		"--format=json output must be parseable JSON: %q", out)
	assert.NotNil(t, doc.Issues, "issues key must be present (never null)")
	// Confirm no spurious table rendering leaked into JSON mode.
	assert.NotContains(t, out, "Contract Health (",
		"text-mode table must not appear in JSON output")
	assert.NotContains(t, out, "PASS: all contracts healthy",
		"text-mode trailing line must not appear in JSON output")
}

// TestCheckContractHealth_TextFormat_HasMethodPathColumns verifies the
// PR239-OB1 enhancement: METHOD and PATH columns appear in the human
// table. Both the header row and at least one HTTP contract row should
// carry the data, so dashboards can read transport metadata directly from
// `gocell check contract-health` output.
func TestCheckContractHealth_TextFormat_HasMethodPathColumns(t *testing.T) {
	out := captureStdout(t, func() {
		_ = runCheck([]string{"contract-health"})
	})
	assert.Contains(t, out, "METHOD",
		"PR239-OB1: text table must have a METHOD column header")
	assert.Contains(t, out, "PATH",
		"PR239-OB1: text table must have a PATH column header")
}

// TestCheckContractHealth_UnknownFormat verifies the dispatcher errors out
// on unknown format strings rather than silently emitting the default —
// catches typos before they become silent CI passes.
func TestCheckContractHealth_UnknownFormat(t *testing.T) {
	err := runCheck([]string{"contract-health", "--format=yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

// TestHTTPTransportColumns covers the cell-table helper directly — both
// branches: HTTP contract with method+path, and non-HTTP / missing
// transport gets "-" placeholders so the table column widths stay stable.
func TestHTTPTransportColumns(t *testing.T) {
	tests := []struct {
		name       string
		c          *metadata.ContractMeta
		wantMethod string
		wantPath   string
	}{
		{
			name: "http with method+path",
			c: &metadata.ContractMeta{
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method: "GET",
						Path:   "/api/v1/things",
					},
				},
			},
			wantMethod: "GET",
			wantPath:   "/api/v1/things",
		},
		{
			name: "http with empty method renders dash",
			c: &metadata.ContractMeta{
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{Path: "/x"},
				},
			},
			wantMethod: "-",
			wantPath:   "/x",
		},
		{
			name: "event contract gets dashes",
			c: &metadata.ContractMeta{
				Kind: "event",
			},
			wantMethod: "-",
			wantPath:   "-",
		},
		{
			name:       "http with nil HTTP transport gets dashes",
			c:          &metadata.ContractMeta{Kind: "http"},
			wantMethod: "-",
			wantPath:   "-",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, path := httpTransportColumns(tt.c)
			assert.Equal(t, tt.wantMethod, method)
			assert.Equal(t, tt.wantPath, path)
		})
	}
}
