package registry_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/kernel/registry"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

// testProject returns a ProjectMeta with realistic test data:
// 2 cells, 3 slices, 4 contracts (one per kind).
func testProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {
				ID:               metadatatest.CellIDAccessCore,
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "identity", Role: "backend"},
			},
			metadatatest.CellIDAuditCore: {
				ID:               metadatatest.CellIDAuditCore,
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "compliance", Role: "backend"},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-create": {
				ID:            "session-create",
				BelongsToCell: metadatatest.CellIDAccessCore,
			},
			"accesscore/session-refresh": {
				ID:            "session-refresh",
				BelongsToCell: metadatatest.CellIDAccessCore,
			},
			"auditcore/audit-write": {
				ID:            "audit-write",
				BelongsToCell: metadatatest.CellIDAuditCore,
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http-auth-login-v1": {
				ID:        "http-auth-login-v1",
				Kind:      "http",
				OwnerCell: metadatatest.CellIDAccessCore,
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.NewCellID("edgegateway"), metadatatest.NewCellID("adminbff")},
				},
			},
			"event-session-created-v1": {
				ID:        "event-session-created-v1",
				Kind:      "event",
				OwnerCell: metadatatest.CellIDAccessCore,
				Endpoints: metadata.EndpointsMeta{
					Publisher:   metadatatest.CellIDAccessCore,
					Subscribers: []string{metadatatest.CellIDAuditCore, metadatatest.CellIDConfigCore},
				},
			},
			"command-audit-archive-v1": {
				ID:        "command-audit-archive-v1",
				Kind:      "command",
				OwnerCell: metadatatest.CellIDAuditCore,
				Endpoints: metadata.EndpointsMeta{
					Handler:  metadatatest.CellIDAuditCore,
					Invokers: []string{metadatatest.CellIDAccessCore},
				},
			},
			"projection-audit-summary-v1": {
				ID:        "projection-audit-summary-v1",
				Kind:      "projection",
				OwnerCell: metadatatest.CellIDAuditCore,
				Endpoints: metadata.EndpointsMeta{
					Provider: metadatatest.CellIDAuditCore,
					Readers:  []string{metadatatest.CellIDAccessCore, metadatatest.CellIDConfigCore},
				},
			},
			"grpc-access-session-verify-v1": {
				ID:        "grpc-access-session-verify-v1",
				Kind:      "grpc",
				OwnerCell: metadatatest.CellIDAccessCore,
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDAuditCore},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// ContractRegistry tests
// ---------------------------------------------------------------------------

func TestContractRegistry_Get(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		wantID string
		found  bool
	}{
		{"existing contract", "http-auth-login-v1", "http-auth-login-v1", true},
		{"another existing", "event-session-created-v1", "event-session-created-v1", true},
		{"not found", "nonexistent", "", false},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Get(tt.id)
			if tt.found {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantID, got.ID)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestContractRegistry_ByKind(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		count int
	}{
		{"http contracts", "http", 1},
		{"event contracts", "event", 1},
		{"command contracts", "command", 1},
		{"projection contracts", "projection", 1},
		{"grpc contracts", "grpc", 1},
		{"unknown kind", "websocket", 0},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.ByKind(tt.kind)
			assert.Len(t, got, tt.count)
		})
	}
}

func TestContractRegistry_ByOwner(t *testing.T) {
	tests := []struct {
		name   string
		cellID string
		count  int
	}{
		{"accesscore owns 3", "accesscore", 3},
		{"auditcore owns 2", "auditcore", 2},
		{"unknown cell", "configcore", 0},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.ByOwner(tt.cellID)
			assert.Len(t, got, tt.count)
		})
	}
}

// TestContractRegistry_ByKind_DeepCopiesGRPC asserts that a ContractMeta
// returned by ByKind carries an independent GRPCTransportMeta — mutating the
// returned copy must not alias-mutate the registry's backing entry.
func TestContractRegistry_ByKind_DeepCopiesGRPC(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"grpc-x-y-v1": {
				ID:   "grpc-x-y-v1",
				Kind: "grpc",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDAuditCore},
					GRPC:    &metadata.GRPCTransportMeta{Service: "x.v1.S", Method: "M", Proto: "contracts/grpc/x/v1/x.proto"},
				},
			},
		},
	}
	reg := registry.NewContractRegistry(proj)
	first := reg.ByKind("grpc")
	require.Len(t, first, 1)
	require.NotNil(t, first[0].Endpoints.GRPC)
	first[0].Endpoints.GRPC.Service = "MUTATED"

	second := reg.ByKind("grpc")
	require.Len(t, second, 1)
	require.NotNil(t, second[0].Endpoints.GRPC)
	assert.Equal(t, "x.v1.S", second[0].Endpoints.GRPC.Service,
		"ByKind must deep-copy GRPCTransportMeta; backing entry was alias-mutated")
}

// TestContractRegistry_ByKind_DeepCopiesHTTP asserts that a returned http
// ContractMeta carries independent HTTPTransportMeta + its maps — mutating the
// returned copy's transport fields/maps must not alias the backing entry.
func TestContractRegistry_ByKind_DeepCopiesHTTP(t *testing.T) {
	reqd := true
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http-x-y-v1": {
				ID:   "http-x-y-v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					Server: metadatatest.CellIDAccessCore,
					HTTP: &metadata.HTTPTransportMeta{
						Method:      "GET",
						Path:        "/api/v1/x/{id}",
						PathParams:  map[string]metadata.ParamSchema{"id": {Type: "string", Required: &reqd}},
						QueryParams: map[string]metadata.ParamSchema{"limit": {Type: "integer"}},
						Responses:   map[int]metadata.HTTPResponseMeta{404: {Description: "not found", SchemaRef: "err.json"}},
					},
				},
			},
		},
	}
	reg := registry.NewContractRegistry(proj)
	first := reg.ByKind("http")
	require.Len(t, first, 1)
	require.NotNil(t, first[0].Endpoints.HTTP)
	first[0].Endpoints.HTTP.Method = "MUTATED"
	first[0].Endpoints.HTTP.PathParams["id"] = metadata.ParamSchema{Type: "MUTATED"}
	first[0].Endpoints.HTTP.QueryParams["limit"] = metadata.ParamSchema{Type: "MUTATED"}
	first[0].Endpoints.HTTP.Responses[404] = metadata.HTTPResponseMeta{Description: "MUTATED"}

	second := reg.ByKind("http")
	require.Len(t, second, 1)
	require.NotNil(t, second[0].Endpoints.HTTP)
	assert.Equal(t, "GET", second[0].Endpoints.HTTP.Method, "Method must be deep-copied")
	assert.Equal(t, "string", second[0].Endpoints.HTTP.PathParams["id"].Type, "PathParams map must be deep-copied")
	assert.Equal(t, "integer", second[0].Endpoints.HTTP.QueryParams["limit"].Type, "QueryParams map must be deep-copied")
	assert.Equal(t, "not found", second[0].Endpoints.HTTP.Responses[404].Description, "Responses map must be deep-copied")
}

// TestContractRegistry_ByKind_Webhook asserts that ByKind resolves the webhook
// kind (the 5th first-class contract kind). It uses its own project rather than
// extending the shared testProject() fixture so the ByOwner / Provider count
// tables stay stable.
func TestContractRegistry_ByKind_Webhook(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"webhook-stripe-events-v1": {
				ID:        "webhook-stripe-events-v1",
				Kind:      "webhook",
				OwnerCell: metadatatest.CellIDAccessCore,
				Direction: "inbound",
				Endpoints: metadata.EndpointsMeta{
					Inbound:   &metadata.WebhookInboundMeta{PathPattern: "/webhooks/stripe", SourceID: "stripe"},
					Receivers: []string{metadatatest.CellIDAccessCore},
				},
			},
		},
	}
	reg := registry.NewContractRegistry(proj)
	got := reg.ByKind("webhook")
	require.Len(t, got, 1)
	assert.Equal(t, "webhook-stripe-events-v1", got[0].ID)
	assert.Empty(t, reg.ByKind("websocket"), "unknown kind must return no contracts")
}

func TestContractRegistry_Provider(t *testing.T) {
	tests := []struct {
		name       string
		contractID string
		want       string
	}{
		{"http provider is server", "http-auth-login-v1", "accesscore"},
		{"event provider is publisher", "event-session-created-v1", "accesscore"},
		{"command provider is handler", "command-audit-archive-v1", "auditcore"},
		{"projection provider is provider", "projection-audit-summary-v1", "auditcore"},
		{"grpc provider is server", "grpc-access-session-verify-v1", "accesscore"},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reg.Provider(tt.contractID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContractRegistry_Provider_NotFound(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got, err := reg.Provider("nonexistent")
	assert.Equal(t, "", got)
	errcodetest.AssertCode(t, err, errcode.ErrContractNotFound)
}

func TestContractRegistry_Consumers(t *testing.T) {
	tests := []struct {
		name       string
		contractID string
		want       []string
	}{
		{"http consumers are clients", "http-auth-login-v1", []string{metadatatest.NewCellID("edgegateway"), metadatatest.NewCellID("adminbff")}},
		{"event consumers are subscribers", "event-session-created-v1", []string{"auditcore", "configcore"}},
		{"command consumers are invokers", "command-audit-archive-v1", []string{"accesscore"}},
		{"projection consumers are readers", "projection-audit-summary-v1", []string{"accesscore", "configcore"}},
		{"grpc consumers are clients", "grpc-access-session-verify-v1", []string{"auditcore"}},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reg.Consumers(tt.contractID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContractRegistry_Consumers_NotFound(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got, err := reg.Consumers("nonexistent")
	assert.Nil(t, got)
	errcodetest.AssertCode(t, err, errcode.ErrContractNotFound)
}

// webhookProject builds a project with one inbound webhook contract whose
// receiver/dispatcher fields + signature/payload pointers are populated so the
// registry's webhook awareness (Provider/Consumers) and deep-copy isolation can
// be exercised.
func webhookProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"webhook-stripe-events-v1": {
				ID:        "webhook-stripe-events-v1",
				Kind:      "webhook",
				Direction: "inbound",
				OwnerCell: metadatatest.CellIDAccessCore,
				Signature: &metadata.WebhookSignatureMeta{Algorithm: "hmac-sha256"},
				Payload:   &metadata.WebhookPayloadMeta{ContentType: "application/json"},
				Endpoints: metadata.EndpointsMeta{
					Inbound:     &metadata.WebhookInboundMeta{PathPattern: "/webhooks/stripe", SourceID: "stripe"},
					Receivers:   []string{metadatatest.CellIDAccessCore},
					Dispatchers: []string{metadatatest.CellIDAuditCore},
				},
			},
		},
	}
}

// TestContractRegistry_WebhookProviderConsumers verifies webhook is in the
// Provider/Consumers closed set: Provider is the ownerCell, Consumers are the
// derived receivers (no "unknown contract kind" error).
func TestContractRegistry_WebhookProviderConsumers(t *testing.T) {
	reg := registry.NewContractRegistry(webhookProject())

	provider, err := reg.Provider("webhook-stripe-events-v1")
	require.NoError(t, err)
	assert.Equal(t, metadatatest.CellIDAccessCore, provider)

	consumers, err := reg.Consumers("webhook-stripe-events-v1")
	require.NoError(t, err)
	assert.Equal(t, []string{metadatatest.CellIDAccessCore}, consumers)
}

// TestContractRegistry_WebhookDeepCopyIsolation verifies the deep copy returned
// by Get does not alias the registry's webhook slices/pointers: mutating the
// copy must not affect a second copy.
func TestContractRegistry_WebhookDeepCopyIsolation(t *testing.T) {
	reg := registry.NewContractRegistry(webhookProject())

	a := reg.Get("webhook-stripe-events-v1")
	require.NotNil(t, a)
	a.Endpoints.Receivers[0] = "mutated"
	a.Endpoints.Dispatchers[0] = "mutated"
	a.Signature.Algorithm = "mutated"
	a.Endpoints.Inbound.SourceID = "mutated"

	b := reg.Get("webhook-stripe-events-v1")
	require.NotNil(t, b)
	assert.Equal(t, metadatatest.CellIDAccessCore, b.Endpoints.Receivers[0],
		"mutating one copy's Receivers must not leak through the registry")
	assert.Equal(t, metadatatest.CellIDAuditCore, b.Endpoints.Dispatchers[0],
		"mutating one copy's Dispatchers must not leak through the registry")
	assert.Equal(t, "hmac-sha256", b.Signature.Algorithm,
		"mutating one copy's Signature must not leak through the registry")
	assert.Equal(t, "stripe", b.Endpoints.Inbound.SourceID,
		"mutating one copy's Inbound must not leak through the registry")
}

func TestContractRegistry_AllIDs(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	ids := reg.AllIDs()
	expected := []string{
		"command-audit-archive-v1",
		"event-session-created-v1",
		"grpc-access-session-verify-v1",
		"http-auth-login-v1",
		"projection-audit-summary-v1",
	}
	assert.Equal(t, expected, ids)
}

func TestContractRegistry_Count(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	assert.Equal(t, 5, reg.Count())
}

func TestContractRegistry_EmptyProject(t *testing.T) {
	tests := []struct {
		name    string
		project *metadata.ProjectMeta
	}{
		{"nil project", nil},
		{"empty project", &metadata.ProjectMeta{}},
		{"nil contracts map", &metadata.ProjectMeta{Contracts: nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.NewContractRegistry(tt.project)
			assert.Equal(t, 0, reg.Count())
			assert.Nil(t, reg.Get("any"))
			assert.Empty(t, reg.ByKind("http"))
			assert.Empty(t, reg.ByOwner("any"))
			_, providerErr := reg.Provider("any")
			require.Error(t, providerErr)
			_, consumersErr := reg.Consumers("any")
			require.Error(t, consumersErr)
			assert.Empty(t, reg.AllIDs())
		})
	}
}

// ---------------------------------------------------------------------------
// CellRegistry tests
// ---------------------------------------------------------------------------

func TestCellRegistry_Get(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		wantID string
		found  bool
	}{
		{"existing cell", "accesscore", "accesscore", true},
		{"another existing", "auditcore", "auditcore", true},
		{"not found", "nonexistent", "", false},
	}
	reg := registry.NewCellRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Get(tt.id)
			if tt.found {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantID, got.ID)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestCellRegistry_SlicesFor(t *testing.T) {
	tests := []struct {
		name   string
		cellID string
		count  int
	}{
		{"accesscore has 2 slices", "accesscore", 2},
		{"auditcore has 1 slice", "auditcore", 1},
		{"unknown cell has 0", "configcore", 0},
	}
	reg := registry.NewCellRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.SlicesFor(tt.cellID)
			assert.Len(t, got, tt.count)
		})
	}
}

func TestCellRegistry_AllIDs(t *testing.T) {
	reg := registry.NewCellRegistry(testProject())
	ids := reg.AllIDs()
	expected := []string{"accesscore", "auditcore"}
	assert.Equal(t, expected, ids)
}

func TestCellRegistry_Count(t *testing.T) {
	reg := registry.NewCellRegistry(testProject())
	assert.Equal(t, 2, reg.Count())
}

func TestCellRegistry_EmptyProject(t *testing.T) {
	tests := []struct {
		name    string
		project *metadata.ProjectMeta
	}{
		{"nil project", nil},
		{"empty project", &metadata.ProjectMeta{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.NewCellRegistry(tt.project)
			assert.Equal(t, 0, reg.Count())
			assert.Nil(t, reg.Get("any"))
			assert.Empty(t, reg.SlicesFor("any"))
			assert.Empty(t, reg.AllIDs())
		})
	}
}

// ---------------------------------------------------------------------------
// Edge-case tests for coverage
// ---------------------------------------------------------------------------

func TestContractRegistry_Provider_UnknownKind(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"websocket-unknown-v1": {
				ID:   "websocket-unknown-v1",
				Kind: "websocket",
			},
		},
	}
	reg := registry.NewContractRegistry(proj)
	got, err := reg.Provider("websocket-unknown-v1")
	require.Error(t, err)
	assert.Equal(t, "", got)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "websocket")
}

func TestContractRegistry_Consumers_UnknownKind(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"websocket-unknown-v1": {
				ID:   "websocket-unknown-v1",
				Kind: "websocket",
			},
		},
	}
	reg := registry.NewContractRegistry(proj)
	got, err := reg.Consumers("websocket-unknown-v1")
	require.Error(t, err)
	assert.Nil(t, got)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "websocket")
}

func TestContractRegistry_NilContractInMap(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"valid": {ID: "valid", Kind: "http"},
			"nil":   nil,
		},
	}
	reg := registry.NewContractRegistry(proj)
	assert.Equal(t, 1, reg.Count())
	assert.Nil(t, reg.Get("nil"))
}

func TestCellRegistry_NilEntries(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.NewCellID("valid"): {ID: metadatatest.NewCellID("valid")},
			metadatatest.NewCellID("nil"):   nil,
		},
		Slices: map[string]*metadata.SliceMeta{
			"valid/s1":  {ID: "s1", BelongsToCell: metadatatest.NewCellID("valid")},
			"valid/nil": nil,
		},
	}
	reg := registry.NewCellRegistry(proj)
	assert.Equal(t, 1, reg.Count())
	assert.Nil(t, reg.Get("nil"))
	assert.Len(t, reg.SlicesFor(metadatatest.NewCellID("valid")), 1)
}

func TestCellRegistry_SliceFallbackCellID(t *testing.T) {
	// Slice with empty BelongsToCell should fall back to parsing composite key.
	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.NewCellID("fallbackcell"): {ID: metadatatest.NewCellID("fallbackcell")},
		},
		Slices: map[string]*metadata.SliceMeta{
			"fallbackcell/orphan-slice": {
				ID: "orphan-slice",
				// BelongsToCell omitted (zero value "") — intentionally empty to test
				// fallback composite-key parsing. Explicit "" omitted to keep A1 clean.
			},
		},
	}
	reg := registry.NewCellRegistry(proj)
	assert.Len(t, reg.SlicesFor(metadatatest.NewCellID("fallbackcell")), 1)
}

// --- Deep-copy mutation tests ---

func TestContractRegistry_Get_DeepCopy(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got := reg.Get("http-auth-login-v1")
	require.NotNil(t, got)

	// Mutate the returned copy.
	got.Endpoints.Clients[0] = "MUTATED"
	got.ID = "MUTATED"

	// Original must be unchanged.
	original := reg.Get("http-auth-login-v1")
	assert.Equal(t, "http-auth-login-v1", original.ID)
	assert.NotEqual(t, "MUTATED", original.Endpoints.Clients[0])
}

func TestContractRegistry_ByKind_DeepCopy(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got := reg.ByKind("http")
	require.Len(t, got, 1)

	got[0].Endpoints.Clients[0] = "MUTATED"

	fresh := reg.ByKind("http")
	assert.NotEqual(t, "MUTATED", fresh[0].Endpoints.Clients[0])
}

func TestContractRegistry_Consumers_DeepCopy(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got, err := reg.Consumers("http-auth-login-v1")
	require.NoError(t, err)
	require.NotEmpty(t, got)

	got[0] = "MUTATED"

	fresh, err := reg.Consumers("http-auth-login-v1")
	require.NoError(t, err)
	assert.NotEqual(t, "MUTATED", fresh[0])
}

func TestCellRegistry_Get_DeepCopy(t *testing.T) {
	proj := testProject()
	proj.Cells["accesscore"].Verify.Smoke = []string{"smoke.startup"}
	reg := registry.NewCellRegistry(proj)
	got := reg.Get("accesscore")
	require.NotNil(t, got)

	got.Verify.Smoke[0] = "MUTATED"
	got.ID = "MUTATED"

	original := reg.Get("accesscore")
	assert.Equal(t, "accesscore", original.ID)
	assert.NotEqual(t, "MUTATED", original.Verify.Smoke[0])
}

func TestCellRegistry_SlicesFor_DeepCopy(t *testing.T) {
	proj := testProject()
	proj.Slices["accesscore/session-create"].ContractUsages = []metadata.ContractUsage{
		{Contract: "http-auth-login-v1", Role: "serve"},
	}
	reg := registry.NewCellRegistry(proj)
	got := reg.SlicesFor("accesscore")
	require.NotEmpty(t, got)

	// Find the slice with contract usages.
	var target *metadata.SliceMeta
	for _, s := range got {
		if len(s.ContractUsages) > 0 {
			target = s
			break
		}
	}
	require.NotNil(t, target)

	target.ContractUsages[0].Role = "MUTATED"

	fresh := reg.SlicesFor("accesscore")
	for _, s := range fresh {
		for _, cu := range s.ContractUsages {
			assert.NotEqual(t, "MUTATED", cu.Role)
		}
	}
}
