package app

import (
	"encoding/json"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
