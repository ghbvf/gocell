package metadata_test

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/kernel/metadata/schemas"
)

// --- ContractUsage YAML unmarshal tests ---

func TestContractUsage_HandlerAndGroupUnmarshal(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantCU metadata.ContractUsage
	}{
		{
			name: "subscribe with handler and group",
			input: `contract: event.session.created.v1
role: subscribe
handler: HandleSessionCreated
group: accesscore-sync`,
			wantCU: metadata.ContractUsage{
				Contract: "event.session.created.v1",
				Role:     "subscribe",
				Handler:  "HandleSessionCreated",
				Group:    "accesscore-sync",
			},
		},
		{
			name: "subscribe with handler only (no group)",
			input: `contract: event.session.created.v1
role: subscribe
handler: HandleSessionCreated`,
			wantCU: metadata.ContractUsage{
				Contract: "event.session.created.v1",
				Role:     "subscribe",
				Handler:  "HandleSessionCreated",
				Group:    "",
			},
		},
		{
			name: "publish role — no handler or group",
			input: `contract: event.session.created.v1
role: publish`,
			wantCU: metadata.ContractUsage{
				Contract: "event.session.created.v1",
				Role:     "publish",
				Handler:  "",
				Group:    "",
			},
		},
		{
			name: "serve role — no handler or group",
			input: `contract: http.auth.login.v1
role: serve`,
			wantCU: metadata.ContractUsage{
				Contract: "http.auth.login.v1",
				Role:     "serve",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got metadata.ContractUsage
			require.NoError(t, yaml.Unmarshal([]byte(tt.input), &got))
			assert.Equal(t, tt.wantCU, got)
		})
	}
}

// --- deriveEventSubscribers tests ---

// buildSubscribeProject builds a ProjectMeta for subscriber derivation tests.
func buildSubscribeProject(slices map[string]*metadata.SliceMeta, contracts map[string]*metadata.ContractMeta) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:      make(map[string]*metadata.CellMeta),
		Slices:     slices,
		Contracts:  contracts,
		Journeys:   make(map[string]*metadata.JourneyMeta),
		Assemblies: make(map[string]*metadata.AssemblyMeta),
	}
}

func TestDeriveEventSubscribers_SingleSubscriber(t *testing.T) {
	pm := buildSubscribeProject(
		map[string]*metadata.SliceMeta{
			"auditcore/auditingest": {
				ID:            "auditingest",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "HandleEvent"},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			"event.session.created.v1": {
				ID:        "event.session.created.v1",
				Kind:      "event",
				Endpoints: metadata.EndpointsMeta{Publisher: metadatatest.CellIDAccessCore, Subscribers: []string{}},
			},
		},
	)

	metadata.ExportedDeriveEventSubscribers(pm)

	subs := pm.Contracts["event.session.created.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{metadatatest.CellIDAuditCore}, subs)
}

func TestDeriveEventSubscribers_MultiCellDedupedSorted(t *testing.T) {
	pm := buildSubscribeProject(
		map[string]*metadata.SliceMeta{
			"auditcore/auditingest": {
				ID:            "auditingest",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "HandleEvent"},
				},
			},
			"configcore/configaudit": {
				ID:            "configaudit",
				BelongsToCell: metadatatest.CellIDConfigCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "HandleEvent"},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			"event.session.created.v1": {
				ID:        "event.session.created.v1",
				Kind:      "event",
				Endpoints: metadata.EndpointsMeta{Publisher: metadatatest.CellIDAccessCore, Subscribers: []string{}},
			},
		},
	)

	metadata.ExportedDeriveEventSubscribers(pm)

	subs := pm.Contracts["event.session.created.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{metadatatest.CellIDAuditCore, metadatatest.CellIDConfigCore}, subs, "must be deduped and sorted")
}

func TestDeriveEventSubscribers_NonEventContractSkipped(t *testing.T) {
	pm := buildSubscribeProject(
		map[string]*metadata.SliceMeta{
			"accesscore/sessionlogin": {
				ID:            "sessionlogin",
				BelongsToCell: metadatatest.CellIDAccessCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "subscribe", Handler: "HandleLogin"},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:        "http.auth.login.v1",
				Kind:      "http", // NOT "event"
				Endpoints: metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore, Subscribers: []string{}},
			},
		},
	)

	metadata.ExportedDeriveEventSubscribers(pm)

	// Subscribers must remain empty — non-event contract skipped
	subs := pm.Contracts["http.auth.login.v1"].Endpoints.Subscribers
	assert.Empty(t, subs)
}

func TestDeriveEventSubscribers_MissingContractSkipped(t *testing.T) {
	pm := buildSubscribeProject(
		map[string]*metadata.SliceMeta{
			"auditcore/auditingest": {
				ID:            "auditingest",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.does.not.exist.v1", Role: "subscribe", Handler: "HandleEvent"},
				},
			},
		},
		map[string]*metadata.ContractMeta{}, // contract missing
	)

	// Must not error; missing contract is simply skipped
	assert.NotPanics(t, func() {
		metadata.ExportedDeriveEventSubscribers(pm)
	})
}

// TestDeriveEventSubscribers_SameCellTwoSlices_Deduped verifies that when two
// slices belonging to the same cell both subscribe to the same event contract,
// the derived Subscribers list contains that cell exactly once. This covers the
// dedup path in deriveEventSubscribers when cellSubs[contractID] accumulates
// the same BelongsToCell value from multiple slices before dedupSorted runs.
func TestDeriveEventSubscribers_SameCellTwoSlices_Deduped(t *testing.T) {
	pm := buildSubscribeProject(
		map[string]*metadata.SliceMeta{
			"auditcore/auditingest": {
				ID:            "auditingest",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "HandleEvent"},
				},
			},
			"auditcore/auditreplay": {
				ID:            "auditreplay",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "HandleReplay"},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			"event.session.created.v1": {
				ID:        "event.session.created.v1",
				Kind:      "event",
				Endpoints: metadata.EndpointsMeta{Publisher: metadatatest.CellIDAccessCore, Subscribers: []string{}},
			},
		},
	)

	metadata.ExportedDeriveEventSubscribers(pm)

	subs := pm.Contracts["event.session.created.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{metadatatest.CellIDAuditCore}, subs,
		"two slices in the same cell subscribing to the same contract must yield exactly one subscriber entry")
}

func TestDeriveEventSubscribers_IdempotentWhenAlreadyListed(t *testing.T) {
	// contract.yaml already declares auditcore — derive must not duplicate it
	pm := buildSubscribeProject(
		map[string]*metadata.SliceMeta{
			"auditcore/auditingest": {
				ID:            "auditingest",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "HandleEvent"},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			"event.session.created.v1": {
				ID:   "event.session.created.v1",
				Kind: "event",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   metadatatest.CellIDAccessCore,
					Subscribers: []string{metadatatest.CellIDAuditCore}, // already declared
				},
			},
		},
	)

	metadata.ExportedDeriveEventSubscribers(pm)

	subs := pm.Contracts["event.session.created.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{metadatatest.CellIDAuditCore}, subs, "union+dedup must not create duplicate")
}

// --- slice.schema.json validation tests ---

func compileSliceSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := schemas.FS.ReadFile("slice.schema.json")
	require.NoError(t, err)

	var doc any
	require.NoError(t, json.Unmarshal(data, &doc))
	compiler := jsonschema.NewCompiler()
	const url = "https://gocell.dev/schemas/slice.schema.json"
	require.NoError(t, compiler.AddResource(url, doc))
	schema, err := compiler.Compile(url)
	require.NoError(t, err)
	return schema
}

func TestSliceSchema_SubscribeWithHandler_Valid(t *testing.T) {
	schema := compileSliceSchema(t)
	doc := map[string]any{
		"id":               "auditingest",
		"consistencyLevel": "L2",
		"contractUsages": []any{
			map[string]any{
				"contract": "event.session.created.v1",
				"role":     "subscribe",
				"handler":  "HandleSessionCreated",
			},
		},
		"verify": map[string]any{
			"unit":     []any{"unit.auditingest.service"},
			"contract": []any{"contract.event.session.created.v1.subscribe"},
		},
	}
	assert.NoError(t, schema.Validate(doc), "subscribe with handler must be valid")
}

func TestSliceSchema_SubscribeWithoutHandler_Invalid(t *testing.T) {
	schema := compileSliceSchema(t)
	doc := map[string]any{
		"id":               "auditingest",
		"consistencyLevel": "L2",
		"contractUsages": []any{
			map[string]any{
				"contract": "event.session.created.v1",
				"role":     "subscribe",
				// handler intentionally missing
			},
		},
		"verify": map[string]any{
			"unit":     []any{},
			"contract": []any{},
		},
	}
	assert.Error(t, schema.Validate(doc), "subscribe without handler must fail schema validation")
}

func TestSliceSchema_HandlerOnNonSubscribeRole_Invalid(t *testing.T) {
	schema := compileSliceSchema(t)
	doc := map[string]any{
		"id":               "sessionlogin",
		"consistencyLevel": "L1",
		"contractUsages": []any{
			map[string]any{
				"contract": "http.auth.login.v1",
				"role":     "serve",
				"handler":  "HandleLogin", // forbidden on non-subscribe
			},
		},
		"verify": map[string]any{
			"unit":     []any{},
			"contract": []any{},
		},
	}
	assert.Error(t, schema.Validate(doc), "handler on non-subscribe role must fail schema validation")
}

func TestSliceSchema_GroupOnNonSubscribeRole_Invalid(t *testing.T) {
	schema := compileSliceSchema(t)
	doc := map[string]any{
		"id":               "sessionlogin",
		"consistencyLevel": "L1",
		"contractUsages": []any{
			map[string]any{
				"contract": "http.auth.login.v1",
				"role":     "publish",
				"group":    "some-group", // forbidden on non-subscribe
			},
		},
		"verify": map[string]any{
			"unit":     []any{},
			"contract": []any{},
		},
	}
	assert.Error(t, schema.Validate(doc), "group on non-subscribe role must fail schema validation")
}

func TestSliceSchema_FieldOnNonSubscribeRole_Invalid(t *testing.T) {
	schema := compileSliceSchema(t)
	doc := map[string]any{
		"id":               "sessionlogin",
		"consistencyLevel": "L1",
		"contractUsages": []any{
			map[string]any{
				"contract": "http.auth.login.v1",
				"role":     "serve",
				"field":    "loginHandler", // forbidden on non-subscribe
			},
		},
		"verify": map[string]any{
			"unit":     []any{},
			"contract": []any{},
		},
	}
	assert.Error(t, schema.Validate(doc), "field on non-subscribe role must fail schema validation")
}

func TestSliceSchema_FieldOnSubscribe_Valid(t *testing.T) {
	schema := compileSliceSchema(t)
	doc := map[string]any{
		"id":               "sessionlogout",
		"consistencyLevel": "L2",
		"contractUsages": []any{
			map[string]any{
				"contract": "event.role.assigned.v1",
				"role":     "subscribe",
				"handler":  "HandleRoleChanged",
				"field":    "rbacSessionConsumer", // optional disambiguator, allowed
			},
		},
		"verify": map[string]any{
			"unit":     []any{},
			"contract": []any{},
		},
	}
	assert.NoError(t, schema.Validate(doc), "field on subscribe role must pass schema validation")
}

func TestSliceSchema_SubscribeWithHandlerAndGroup_Valid(t *testing.T) {
	schema := compileSliceSchema(t)
	doc := map[string]any{
		"id":               "auditingest",
		"consistencyLevel": "L2",
		"contractUsages": []any{
			map[string]any{
				"contract": "event.session.created.v1",
				"role":     "subscribe",
				"handler":  "HandleSessionCreated",
				"group":    "auditcore-ingest",
			},
		},
		"verify": map[string]any{
			"unit":     []any{},
			"contract": []any{},
		},
	}
	assert.NoError(t, schema.Validate(doc), "subscribe with handler and group must be valid")
}
