package app

import (
	"strings"
	"testing"

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
					OwnerCell: "order-cell",
					Lifecycle: "active",
					SchemaRefs: metadata.SchemaRefsMeta{
						Request:  "request.schema.json",
						Response: "response.schema.json",
					},
				},
				{
					ID:        "event.order-created.v1",
					Kind:      "event",
					OwnerCell: "order-cell",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := validateContractHealth(tt.contracts)
			if tt.wantErr {
				require.NotEmpty(t, issues, "expected validation issues")
				found := false
				for _, issue := range issues {
					if strings.Contains(issue, tt.wantMsg) {
						found = true
						break
					}
				}
				assert.True(t, found, "expected issue containing %q, got: %v", tt.wantMsg, issues)
			} else {
				assert.Empty(t, issues, "expected no validation issues")
			}
		})
	}
}

func TestCheckContractHealthCI(t *testing.T) {
	// Run against the real project — should pass with 0 issues.
	err := runCheck([]string{"contract-health"})
	assert.NoError(t, err, "contract-health should pass on the project's contracts")
}
