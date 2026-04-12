package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// roundTrip marshals v to YAML, unmarshals into a new T, and returns both the
// YAML bytes and the decoded value.
func roundTrip[T any](t *testing.T, v T) ([]byte, T) {
	t.Helper()
	data, err := yaml.Marshal(v)
	require.NoError(t, err, "marshal should succeed")

	var got T
	err = yaml.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal should succeed")
	return data, got
}

func TestCellMetaRoundTrip(t *testing.T) {
	orig := CellMeta{
		ID:               "access-core",
		Type:             "core",
		ConsistencyLevel: "L2",
		Owner:            OwnerMeta{Team: "platform", Role: "cell-owner"},
		Schema:           SchemaMeta{Primary: "cell_access_core"},
		Verify:           CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
		L0Dependencies:   []L0DepMeta{{Cell: "shared-crypto", Reason: "hashing"}},
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestCellMetaEmptyL0Dependencies(t *testing.T) {
	orig := CellMeta{
		ID:               "config-core",
		Type:             "core",
		ConsistencyLevel: "L2",
		Owner:            OwnerMeta{Team: "platform", Role: "cell-owner"},
		Schema:           SchemaMeta{Primary: "cell_config_core"},
		Verify:           CellVerifyMeta{Smoke: []string{"smoke.config-core.startup"}},
		L0Dependencies:   []L0DepMeta{},
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestSliceMetaRoundTrip(t *testing.T) {
	orig := SliceMeta{
		ID:            "session-login",
		BelongsToCell: "access-core",
		ContractUsages: []ContractUsage{
			{Contract: "http.auth.login.v1", Role: "serve"},
			{Contract: "event.session.created.v1", Role: "publish"},
		},
		Verify: SliceVerifyMeta{
			Unit:     []string{"unit.session-login.service"},
			Contract: []string{"contract.http.auth.login.v1.serve"},
			Waivers: []WaiverMeta{
				{
					Contract:  "http.config.get.v1",
					Owner:     "platform-team",
					Reason:    "read-only config call",
					ExpiresAt: "2026-06-01",
				},
			},
		},
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestContractMetaHTTPRoundTrip(t *testing.T) {
	orig := ContractMeta{
		ID:               "http.auth.login.v1",
		Kind:             "http",
		OwnerCell:        "access-core",
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: EndpointsMeta{
			Server:  "access-core",
			Clients: []string{"edge-bff"},
		},
		SchemaRefs: SchemaRefsMeta{
			Request:  "request.schema.json",
			Response: "response.schema.json",
		},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)

	// replayable / idempotencyKey / deliverySemantics should be absent
	assert.NotContains(t, string(data), "replayable")
	assert.NotContains(t, string(data), "idempotencyKey")
	assert.NotContains(t, string(data), "deliverySemantics")
}

func TestContractMetaHTTPTransportRoundTrip(t *testing.T) {
	orig := ContractMeta{
		ID:               "http.auth.user.delete.v1",
		Kind:             "http",
		OwnerCell:        "access-core",
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: EndpointsMeta{
			Server:  "access-core",
			Clients: []string{"edge-bff"},
			HTTP: &HTTPTransportMeta{
				Method:        "DELETE",
				Path:          "/api/v1/auth/users/{userId}",
				SuccessStatus: 204,
				NoContent:     true,
			},
		},
		SchemaRefs: SchemaRefsMeta{
			Request: "request.schema.json",
		},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "http:")
	assert.Contains(t, string(data), "method: DELETE")
	assert.Contains(t, string(data), "path: /api/v1/auth/users/{userId}")
	assert.Contains(t, string(data), "successStatus: 204")
	assert.Contains(t, string(data), "noContent: true")
}

func TestContractMetaEventRoundTrip(t *testing.T) {
	replayable := true
	orig := ContractMeta{
		ID:               "event.session.created.v1",
		Kind:             "event",
		OwnerCell:        "access-core",
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints: EndpointsMeta{
			Publisher:   "access-core",
			Subscribers: []string{"audit-core"},
		},
		Replayable:        &replayable,
		IdempotencyKey:    "event_id",
		DeliverySemantics: "at-least-once",
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)

	// Event-specific fields should be present
	assert.Contains(t, string(data), "replayable")
	assert.Contains(t, string(data), "idempotencyKey")
	assert.Contains(t, string(data), "deliverySemantics")

	// SchemaRefs should be absent (omitempty)
	assert.NotContains(t, string(data), "schemaRefs")
}

func TestContractMetaOmitEmptySchemaRefs(t *testing.T) {
	orig := ContractMeta{
		ID:               "http.test.v1",
		Kind:             "http",
		OwnerCell:        "test-cell",
		ConsistencyLevel: "L1",
		Lifecycle:        "draft",
		Endpoints: EndpointsMeta{
			Server: "test-cell",
		},
	}
	data, _ := roundTrip(t, orig)
	assert.NotContains(t, string(data), "schemaRefs")
}

// TestSchemaRefsInlinePrecedence verifies that when a YAML schemaRefs block
// contains both named keys (request, response, payload) and extra keys, the
// decoder fills named struct fields first — Extra never shadows them.
func TestSchemaRefsInlinePrecedence(t *testing.T) {
	raw := `request: req.json
response: res.json
custom: extra.json
`
	var sr SchemaRefsMeta
	require.NoError(t, yaml.Unmarshal([]byte(raw), &sr))

	// Named fields populated
	assert.Equal(t, "req.json", sr.Request)
	assert.Equal(t, "res.json", sr.Response)
	assert.Empty(t, sr.Payload)

	// Extra captures only the unknown key
	assert.Equal(t, map[string]string{"custom": "extra.json"}, sr.Extra)

	// Named key must NOT appear in Extra
	_, hasRequest := sr.Extra["request"]
	assert.False(t, hasRequest, "named field 'request' must not leak into Extra")
}

// TestSchemaRefsExtraRoundTrip verifies that Extra keys survive marshal→unmarshal.
func TestSchemaRefsExtraRoundTrip(t *testing.T) {
	orig := SchemaRefsMeta{
		Request: "req.json",
		Extra:   map[string]string{"custom": "extra.json"},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, "req.json", got.Request)
	assert.Equal(t, "extra.json", got.Extra["custom"])
	assert.Contains(t, string(data), "custom: extra.json")
}

func TestContractMetaNilReplayable(t *testing.T) {
	orig := ContractMeta{
		ID:               "http.test.v1",
		Kind:             "http",
		OwnerCell:        "test-cell",
		ConsistencyLevel: "L1",
		Lifecycle:        "draft",
		Endpoints: EndpointsMeta{
			Server: "test-cell",
		},
	}
	data, got := roundTrip(t, orig)
	assert.Nil(t, got.Replayable)
	assert.NotContains(t, string(data), "replayable")
}

func TestJourneyMetaRoundTrip(t *testing.T) {
	orig := JourneyMeta{
		ID:    "J-sso-login",
		Goal:  "User completes SSO login",
		Owner: OwnerMeta{Team: "platform", Role: "journey-owner"},
		Cells: []string{"access-core", "audit-core"},
		Contracts: []string{
			"http.auth.login.v1",
			"event.session.created.v1",
		},
		PassCriteria: []PassCriterion{
			{Text: "OIDC redirect completed", Mode: "auto", CheckRef: "journey.J-sso-login.oidc-redirect"},
			{Text: "Security review", Mode: "manual"},
		},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)

	// Manual criterion should not have checkRef in output
	assert.Contains(t, string(data), "mode: manual")
}

func TestPassCriterionOmitEmptyCheckRef(t *testing.T) {
	orig := PassCriterion{Text: "Manual check", Mode: "manual"}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.NotContains(t, string(data), "checkRef")
}

func TestAssemblyMetaRoundTrip(t *testing.T) {
	orig := AssemblyMeta{
		ID:    "core-bundle",
		Cells: []string{"access-core", "audit-core", "config-core"},
		Build: BuildMeta{
			Entrypoint:     "src/cmd/core-bundle/main.go",
			Binary:         "core-bundle",
			DeployTemplate: "k8s",
		},
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestStatusBoardEntryRoundTrip(t *testing.T) {
	orig := StatusBoardEntry{
		JourneyID: "J-sso-login",
		State:     "doing",
		Risk:      "low",
		Blocker:   "",
		UpdatedAt: "2026-04-04",
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestActorMetaRoundTrip(t *testing.T) {
	orig := ActorMeta{
		ID:                  "edge-bff",
		Type:                "external",
		MaxConsistencyLevel: "L1",
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestEndpointsMetaOmitEmpty(t *testing.T) {
	tests := []struct {
		name     string
		meta     EndpointsMeta
		present  []string
		absent   []string
	}{
		{
			name: "http only",
			meta: EndpointsMeta{Server: "cell-a", Clients: []string{"cell-b"}},
			present: []string{"server", "clients"},
			absent:  []string{"http", "publisher", "subscribers", "handler", "invokers", "provider", "readers"},
		},
		{
			name: "http with transport",
			meta: EndpointsMeta{
				Server:  "cell-a",
				Clients: []string{"cell-b"},
				HTTP: &HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/test",
					SuccessStatus: 200,
					NoContent:     false,
				},
			},
			present: []string{"server", "clients", "http", "method", "path", "successStatus", "noContent"},
			absent:  []string{"publisher", "subscribers", "handler", "invokers", "provider", "readers"},
		},
		{
			name: "event only",
			meta: EndpointsMeta{Publisher: "cell-a", Subscribers: []string{"cell-b"}},
			present: []string{"publisher", "subscribers"},
			absent:  []string{"server", "clients", "http", "handler", "invokers", "provider", "readers"},
		},
		{
			name: "command only",
			meta: EndpointsMeta{Handler: "cell-a", Invokers: []string{"cell-b"}},
			present: []string{"handler", "invokers"},
			absent:  []string{"server", "clients", "http", "publisher", "subscribers", "provider", "readers"},
		},
		{
			name: "projection only",
			meta: EndpointsMeta{Provider: "cell-a", Readers: []string{"cell-b"}},
			present: []string{"provider", "readers"},
			absent:  []string{"server", "clients", "http", "publisher", "subscribers", "handler", "invokers"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, got := roundTrip(t, tt.meta)
			assert.Equal(t, tt.meta, got)
			s := string(data)
			for _, field := range tt.present {
				assert.Contains(t, s, field)
			}
			for _, field := range tt.absent {
				assert.NotContains(t, s, field)
			}
		})
	}
}

func TestStatusBoardSliceRoundTrip(t *testing.T) {
	orig := []StatusBoardEntry{
		{JourneyID: "J-sso-login", State: "doing", Risk: "low", Blocker: "", UpdatedAt: "2026-04-04"},
		{JourneyID: "J-session-refresh", State: "todo", Risk: "low", Blocker: "", UpdatedAt: "2026-04-05"},
	}
	data, err := yaml.Marshal(orig)
	require.NoError(t, err)

	var got []StatusBoardEntry
	err = yaml.Unmarshal(data, &got)
	require.NoError(t, err)
	assert.Equal(t, orig, got)
}

func TestContractMeta_ProviderEndpoint(t *testing.T) {
	tests := []struct {
		name string
		meta ContractMeta
		want string
	}{
		{"http returns server", ContractMeta{Kind: "http", Endpoints: EndpointsMeta{Server: "cell-a"}}, "cell-a"},
		{"event returns publisher", ContractMeta{Kind: "event", Endpoints: EndpointsMeta{Publisher: "cell-b"}}, "cell-b"},
		{"command returns handler", ContractMeta{Kind: "command", Endpoints: EndpointsMeta{Handler: "cell-c"}}, "cell-c"},
		{"projection returns provider", ContractMeta{Kind: "projection", Endpoints: EndpointsMeta{Provider: "cell-d"}}, "cell-d"},
		{"unknown kind returns empty", ContractMeta{Kind: "grpc"}, ""},
		{"empty kind returns empty", ContractMeta{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.meta.ProviderEndpoint())
		})
	}
}

func TestActorSliceRoundTrip(t *testing.T) {
	orig := []ActorMeta{
		{ID: "edge-bff", Type: "external", MaxConsistencyLevel: "L1"},
	}
	data, err := yaml.Marshal(orig)
	require.NoError(t, err)

	var got []ActorMeta
	err = yaml.Unmarshal(data, &got)
	require.NoError(t, err)
	assert.Equal(t, orig, got)
}
