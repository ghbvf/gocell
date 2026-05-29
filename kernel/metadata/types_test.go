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
	orig := metadata.CellMeta{
		ID:               metadatatest.CellIDAccessCore,
		Type:             "core",
		ConsistencyLevel: "L2",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
		Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
		L0Dependencies:   []metadata.L0DepMeta{{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"}},
		Requires:         []string{"postgres", "redis"},
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

// TestCellMeta_Clone_RequiresIndependence asserts that Clone deep-copies the
// Requires slice (Design Y, #855): mutating the clone's Requires must not leak
// into the original. Mirrors the L0Dependencies / Verify.Smoke deep-copy guard.
func TestCellMeta_Clone_RequiresIndependence(t *testing.T) {
	src := &metadata.CellMeta{
		ID:       metadatatest.CellIDAccessCore,
		Requires: []string{"postgres", "redis"},
	}
	clone := src.Clone()
	require.Equal(t, src.Requires, clone.Requires)

	clone.Requires[0] = "MUTATED"
	clone.Requires = append(clone.Requires, "rabbitmq")
	assert.Equal(t, []string{"postgres", "redis"}, src.Requires,
		"mutating clone.Requires must not affect the original")
}

func TestCellMetaEmptyL0Dependencies(t *testing.T) {
	orig := metadata.CellMeta{
		ID:               metadatatest.CellIDConfigCore,
		Type:             "core",
		ConsistencyLevel: "L2",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
		Schema:           metadata.SchemaMeta{Primary: "cell_config_core"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.configcore.startup"}},
		L0Dependencies:   []metadata.L0DepMeta{},
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestSliceMetaRoundTrip(t *testing.T) {
	orig := metadata.SliceMeta{
		ID:            "session-login",
		BelongsToCell: metadatatest.CellIDAccessCore,
		ContractUsages: []metadata.ContractUsage{
			{Contract: "http.auth.login.v1", Role: "serve"},
			{Contract: "event.session.created.v1", Role: "publish"},
		},
		Verify: metadata.SliceVerifyMeta{
			Unit:     []string{"unit.session-login.service"},
			Contract: []string{"contract.http.auth.login.v1.serve"},
			Waivers: []metadata.WaiverMeta{
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
	orig := metadata.ContractMeta{
		ID:               "http.auth.login.v1",
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDAccessCore,
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server:  metadatatest.CellIDAccessCore,
			Clients: []string{metadatatest.CellIDEdgeBFF},
		},
		SchemaRefs: metadata.SchemaRefsMeta{
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
	orig := metadata.ContractMeta{
		ID:               "http.auth.user.delete.v1",
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDAccessCore,
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server:  metadatatest.CellIDAccessCore,
			Clients: []string{metadatatest.CellIDEdgeBFF},
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "DELETE",
				Path:          "/api/v1/auth/users/{userId}",
				SuccessStatus: 204,
				NoContent:     true,
			},
		},
		SchemaRefs: metadata.SchemaRefsMeta{
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
	// Subscribers is yaml:"-" (derived, not persisted). Round-trip via
	// actorSubscribers only; cell subscribers are derived at parse time.
	orig := metadata.ContractMeta{
		ID:               "event.session.created.v1",
		Kind:             "event",
		OwnerCell:        metadatatest.CellIDAccessCore,
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Publisher:        metadatatest.CellIDAccessCore,
			ActorSubscribers: []string{"external-sink"},
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
	assert.Contains(t, string(data), "actorSubscribers")

	// subscribers must not appear in marshaled output (yaml:"-")
	assert.NotContains(t, string(data), "subscribers:")

	// SchemaRefs should be absent (omitempty)
	assert.NotContains(t, string(data), "schemaRefs")
}

func TestContractMetaOmitEmptySchemaRefs(t *testing.T) {
	orig := metadata.ContractMeta{
		ID:               "http.test.v1",
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDTestCell,
		ConsistencyLevel: "L1",
		Lifecycle:        "draft",
		Endpoints: metadata.EndpointsMeta{
			Server: metadatatest.CellIDTestCell,
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
	var sr metadata.SchemaRefsMeta
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
	orig := metadata.SchemaRefsMeta{
		Request: "req.json",
		Extra:   map[string]string{"custom": "extra.json"},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, "req.json", got.Request)
	assert.Equal(t, "extra.json", got.Extra["custom"])
	assert.Contains(t, string(data), "custom: extra.json")
}

func TestContractMetaNilReplayable(t *testing.T) {
	orig := metadata.ContractMeta{
		ID:               "http.test.v1",
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDTestCell,
		ConsistencyLevel: "L1",
		Lifecycle:        "draft",
		Endpoints: metadata.EndpointsMeta{
			Server: metadatatest.CellIDTestCell,
		},
	}
	data, got := roundTrip(t, orig)
	assert.Nil(t, got.Replayable)
	assert.NotContains(t, string(data), "replayable")
}

func TestJourneyMetaRoundTrip(t *testing.T) {
	orig := metadata.JourneyMeta{
		ID:        "J-ssologin",
		Goal:      "User completes SSO login",
		Lifecycle: "active",
		Owner:     metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
		Cells:     []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore},
		Contracts: []string{
			"http.auth.login.v1",
			"event.session.created.v1",
		},
		PassCriteria: []metadata.PassCriterion{
			{Text: "OIDC redirect completed", Mode: "auto", CheckRef: "journey.J-ssologin.oidc-redirect"},
			{Text: "Security review", Mode: "manual"},
		},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "lifecycle: active")

	// Manual criterion should not have checkRef in output
	assert.Contains(t, string(data), "mode: manual")
}

func TestPassCriterionOmitEmptyCheckRef(t *testing.T) {
	orig := metadata.PassCriterion{Text: "Manual check", Mode: "manual"}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.NotContains(t, string(data), "checkRef")
}

func TestJourneySchemaRequiresLifecycle(t *testing.T) {
	data, err := schemas.FS.ReadFile("journey.schema.json")
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	required, ok := doc["required"].([]any)
	require.True(t, ok)
	assert.Contains(t, required, "lifecycle")

	properties, ok := doc["properties"].(map[string]any)
	require.True(t, ok)
	lifecycle, ok := properties["lifecycle"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"active", "experimental"}, lifecycle["enum"])
}

func TestJourneySchemaLeavesActiveAutoGateToStrictValidation(t *testing.T) {
	schema := loadJourneySchema(t)
	doc := map[string]any{
		"id":        "J-manual",
		"goal":      "manual active journey is structurally valid",
		"lifecycle": "active",
		"owner": map[string]any{
			"team": "platform",
			"role": "journey-owner",
		},
		"cells": []any{"accesscore"},
		"passCriteria": []any{
			map[string]any{"text": "manual signoff", "mode": "manual"},
		},
	}
	assert.NoError(t, schema.Validate(doc))
}

func TestJourneySchemaRejectsManualCheckRef(t *testing.T) {
	schema := loadJourneySchema(t)
	doc := map[string]any{
		"id":        "J-manual",
		"goal":      "manual checkRef is forbidden",
		"lifecycle": "experimental",
		"owner": map[string]any{
			"team": "platform",
			"role": "journey-owner",
		},
		"cells": []any{"accesscore"},
		"passCriteria": []any{
			map[string]any{"text": "manual signoff", "mode": "manual", "checkRef": "journey.J-manual.signoff"},
		},
	}
	assert.Error(t, schema.Validate(doc))
}

func loadJourneySchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := schemas.FS.ReadFile("journey.schema.json")
	require.NoError(t, err)

	var doc any
	require.NoError(t, json.Unmarshal(data, &doc))
	compiler := jsonschema.NewCompiler()
	const url = "file:///journey.schema.json"
	require.NoError(t, compiler.AddResource(url, doc))
	schema, err := compiler.Compile(url)
	require.NoError(t, err)
	return schema
}

func TestAssemblyMetaRoundTrip(t *testing.T) {
	orig := metadata.AssemblyMeta{
		ID:    "corebundle",
		Cells: []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore, metadatatest.CellIDConfigCore},
		Build: metadata.BuildMeta{
			Entrypoint:     "cmd/corebundle/main.go",
			Binary:         "corebundle",
			DeployTemplate: "k8s",
		},
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestStatusBoardEntryRoundTrip(t *testing.T) {
	orig := metadata.StatusBoardEntry{
		JourneyID: "J-ssologin",
		State:     "doing",
		Risk:      "low",
		Blocker:   "",
		UpdatedAt: "2026-04-04",
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestActorMetaRoundTrip(t *testing.T) {
	orig := metadata.ActorMeta{
		ID:                  "edge-bff",
		MaxConsistencyLevel: "L1",
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestHTTPTransportMetaResponsesRoundTrip(t *testing.T) {
	orig := metadata.HTTPTransportMeta{
		Method:        "POST",
		Path:          "/api/v1/test",
		SuccessStatus: 200,
		NoContent:     false,
		Responses: map[int]metadata.HTTPResponseMeta{
			401: {Description: "Unauthorized", SchemaRef: "error.json"},
			403: {Description: "Forbidden", SchemaRef: "error.json"},
		},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "responses")
	assert.Contains(t, string(data), "description: Unauthorized")
}

func TestHTTPTransportMetaResponsesOmitEmpty(t *testing.T) {
	orig := metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/test",
		SuccessStatus: 200,
		NoContent:     false,
	}
	data, _ := roundTrip(t, orig)
	assert.NotContains(t, string(data), "responses")
}

func TestEndpointsMetaOmitEmpty(t *testing.T) {
	tests := []struct {
		name    string
		meta    metadata.EndpointsMeta
		present []string
		absent  []string
	}{
		{
			name:    "http only",
			meta:    metadata.EndpointsMeta{Server: metadatatest.NewCellID("cella"), Clients: []string{metadatatest.NewCellID("cellb")}},
			present: []string{"server", "clients"},
			absent:  []string{"http", "publisher", "subscribers", "handler", "invokers", "provider", "readers"},
		},
		{
			name: "http with transport",
			meta: metadata.EndpointsMeta{
				Server:  metadatatest.NewCellID("cella"),
				Clients: []string{metadatatest.NewCellID("cellb")},
				HTTP: &metadata.HTTPTransportMeta{
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
			// Subscribers is yaml:"-" (derived, not persisted in YAML).
			// Use actorSubscribers to exercise the event endpoints round-trip.
			name:    "event only",
			meta:    metadata.EndpointsMeta{Publisher: metadatatest.NewCellID("cella"), ActorSubscribers: []string{"external-sink"}},
			present: []string{"publisher", "actorSubscribers"},
			absent:  []string{"server", "clients", "http", "handler", "invokers", "provider", "readers", "subscribers"},
		},
		{
			name:    "command only",
			meta:    metadata.EndpointsMeta{Handler: metadatatest.NewCellID("cella"), Invokers: []string{metadatatest.NewCellID("cellb")}},
			present: []string{"handler", "invokers"},
			absent:  []string{"server", "clients", "http", "publisher", "subscribers", "provider", "readers"},
		},
		{
			name:    "projection only",
			meta:    metadata.EndpointsMeta{Provider: metadatatest.NewCellID("cella"), Readers: []string{metadatatest.NewCellID("cellb")}},
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
	orig := []metadata.StatusBoardEntry{
		{JourneyID: "J-ssologin", State: "doing", Risk: "low", Blocker: "", UpdatedAt: "2026-04-04"},
		{JourneyID: "J-sessionrefresh", State: "todo", Risk: "low", Blocker: "", UpdatedAt: "2026-04-05"},
	}
	data, err := yaml.Marshal(orig)
	require.NoError(t, err)

	var got []metadata.StatusBoardEntry
	err = yaml.Unmarshal(data, &got)
	require.NoError(t, err)
	assert.Equal(t, orig, got)
}

func TestContractMeta_ProviderEndpoint(t *testing.T) {
	tests := []struct {
		name string
		meta metadata.ContractMeta
		want string
	}{
		{
			"http returns server",
			metadata.ContractMeta{Kind: "http", Endpoints: metadata.EndpointsMeta{Server: metadatatest.NewCellID("cella")}},
			metadatatest.NewCellID("cella"),
		},
		{
			"event returns publisher",
			metadata.ContractMeta{Kind: "event", Endpoints: metadata.EndpointsMeta{Publisher: metadatatest.NewCellID("cellb")}},
			metadatatest.NewCellID("cellb"),
		},
		{
			"command returns handler",
			metadata.ContractMeta{Kind: "command", Endpoints: metadata.EndpointsMeta{Handler: metadatatest.NewCellID("cellc")}},
			metadatatest.NewCellID("cellc"),
		},
		{
			"projection returns provider",
			metadata.ContractMeta{Kind: "projection", Endpoints: metadata.EndpointsMeta{Provider: metadatatest.NewCellID("celld")}},
			metadatatest.NewCellID("celld"),
		},
		{
			"grpc returns server",
			metadata.ContractMeta{Kind: "grpc", Endpoints: metadata.EndpointsMeta{Server: metadatatest.NewCellID("celle")}},
			metadatatest.NewCellID("celle"),
		},
		{"unknown kind returns empty", metadata.ContractMeta{Kind: "websocket"}, ""},
		{"empty kind returns empty", metadata.ContractMeta{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.meta.ProviderEndpoint())
		})
	}
}

// TestSliceMeta_Clone_Independence asserts that Clone produces an independent
// deep copy — mutations to the source do not affect the clone and vice-versa.
// This mirrors the K8s zz_generated.deepcopy.go independence guarantee.
func TestSliceMeta_Clone_Independence(t *testing.T) {
	src := &metadata.SliceMeta{
		ID:               "sessionlogin",
		BelongsToCell:    metadatatest.CellIDAccessCore,
		ConsistencyLevel: "L2",
		ContractUsages: []metadata.ContractUsage{
			{Contract: "http.auth.login.v1", Role: "serve"},
			{Contract: "event.session.created.v1", Role: "publish"},
		},
		Verify: metadata.SliceVerifyMeta{
			Unit:     []string{"unit.sessionlogin.service"},
			Contract: []string{"contract.http.auth.login.v1.serve"},
			Waivers: []metadata.WaiverMeta{
				{
					Contract: "http.config.get.v1", Owner: "platform-team",
					Reason: "read-only config call", ExpiresAt: "2026-06-01",
				},
			},
		},
		AllowedFiles: []string{"cells/accesscore/slices/sessionlogin/**"},
		Dir:          "sessionlogin",
		CellDir:      "accesscore",
		File:         "cells/accesscore/slices/sessionlogin/slice.yaml",
	}

	clone := src.Clone()

	// Clone must equal source initially.
	assert.Equal(t, src.ID, clone.ID)
	assert.Equal(t, src.BelongsToCell, clone.BelongsToCell)
	assert.Equal(t, src.ConsistencyLevel, clone.ConsistencyLevel)
	assert.Equal(t, src.ContractUsages, clone.ContractUsages)
	assert.Equal(t, src.Verify.Unit, clone.Verify.Unit)
	assert.Equal(t, src.Verify.Contract, clone.Verify.Contract)
	assert.Equal(t, src.Verify.Waivers, clone.Verify.Waivers)
	assert.Equal(t, src.AllowedFiles, clone.AllowedFiles)

	// Mutate source slices — clone must be unaffected.
	src.ContractUsages[0].Role = "MUTATED"
	src.Verify.Unit[0] = "MUTATED"
	src.Verify.Contract[0] = "MUTATED"
	src.Verify.Waivers[0].Owner = "MUTATED"
	src.AllowedFiles[0] = "MUTATED"

	assert.Equal(t, "serve", clone.ContractUsages[0].Role, "clone ContractUsages must be independent of source")
	assert.Equal(t, "unit.sessionlogin.service", clone.Verify.Unit[0], "clone Verify.Unit must be independent")
	assert.Equal(t, "contract.http.auth.login.v1.serve", clone.Verify.Contract[0], "clone Verify.Contract must be independent")
	assert.Equal(t, "platform-team", clone.Verify.Waivers[0].Owner, "clone Verify.Waivers must be independent")
	assert.Equal(t, "cells/accesscore/slices/sessionlogin/**", clone.AllowedFiles[0], "clone AllowedFiles must be independent")

	// Mutate clone — source must be unaffected (values already mutated above, reset check on a fresh pair).
	src2 := &metadata.SliceMeta{
		ContractUsages: []metadata.ContractUsage{{Contract: "c.v1", Role: "serve"}},
		Verify:         metadata.SliceVerifyMeta{Unit: []string{"u1"}, Contract: []string{"c1"}},
		AllowedFiles:   []string{"files/**"},
	}
	clone2 := src2.Clone()
	clone2.ContractUsages[0].Role = "CLONE-MUTATED"
	clone2.Verify.Unit[0] = "CLONE-MUTATED"
	clone2.AllowedFiles[0] = "CLONE-MUTATED"

	assert.Equal(t, "serve", src2.ContractUsages[0].Role, "source ContractUsages must be independent of clone")
	assert.Equal(t, "u1", src2.Verify.Unit[0], "source Verify.Unit must be independent of clone")
	assert.Equal(t, "files/**", src2.AllowedFiles[0], "source AllowedFiles must be independent of clone")
}

// TestSliceMeta_Clone_Nil asserts that nil.Clone() returns nil without panicking.
func TestSliceMeta_Clone_Nil(t *testing.T) {
	var s *metadata.SliceMeta
	assert.Nil(t, s.Clone(), "nil SliceMeta.Clone() must return nil")
}

func TestActorSliceRoundTrip(t *testing.T) {
	orig := []metadata.ActorMeta{
		{ID: "edge-bff", MaxConsistencyLevel: "L1"},
	}
	data, err := yaml.Marshal(orig)
	require.NoError(t, err)

	var got []metadata.ActorMeta
	err = yaml.Unmarshal(data, &got)
	require.NoError(t, err)
	assert.Equal(t, orig, got)
}
