package governance

// webhook_governance_test.go verifies that the governance engine correctly
// handles the new "webhook" contract kind:
//   - validKinds map contains "webhook"
//   - validRoles map contains "webhook-receive" and "webhook-dispatch"
//   - consumerFieldName("webhook") returns "receivers" (inbound consumers)
//   - contractConsumers on a webhook contract returns Endpoints.Receivers
//   - FMT-01 (invalid lifecycle) and FMT-03 (invalid kind) produce correct
//     results for webhook contracts so the webhook kind is treated as a first-
//     class kind throughout the governance engine.
//
// These tests are the RED baseline; they reference package-level vars and
// methods that the impl agent will update.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// TestValidKindsContainsWebhook asserts that the package-level validKinds map
// (consulted by FMT-03) includes the "webhook" kind after the impl adds it.
func TestValidKindsContainsWebhook(t *testing.T) {
	assert.True(t, validKinds[string(cellvocab.ContractWebhook)],
		"validKinds must contain ContractWebhook=%q", string(cellvocab.ContractWebhook))
}

// TestValidRolesContainsWebhookRoles asserts that the package-level validRoles
// map (consulted by FMT-05) includes both webhook roles after the impl adds them.
func TestValidRolesContainsWebhookRoles(t *testing.T) {
	assert.True(t, validRoles[string(cellvocab.RoleWebhookReceive)],
		"validRoles must contain RoleWebhookReceive=%q", string(cellvocab.RoleWebhookReceive))
	assert.True(t, validRoles[string(cellvocab.RoleWebhookDispatch)],
		"validRoles must contain RoleWebhookDispatch=%q", string(cellvocab.RoleWebhookDispatch))
}

// TestConsumerFieldNameWebhook verifies that consumerFieldName("webhook")
// returns "receivers" — the consumer field for webhook contracts.
func TestConsumerFieldNameWebhook(t *testing.T) {
	got := consumerFieldName("webhook")
	assert.Equal(t, "receivers", got,
		"consumerFieldName(webhook) should return %q", "receivers")
}

// TestContractConsumersWebhook verifies that contractConsumers returns
// Endpoints.Receivers for webhook contracts.
func TestContractConsumersWebhook(t *testing.T) {
	c := &metadata.ContractMeta{
		ID:   "webhook.stripe.events.v1",
		Kind: "webhook",
		Endpoints: metadata.EndpointsMeta{
			Receivers: []string{metadatatest.NewCellID("paymentcore")},
		},
	}
	got := contractConsumers(c)
	assert.Equal(t, []string{metadatatest.NewCellID("paymentcore")}, got)
}

// TestFMT01_WebhookContractValidLifecycle verifies that a webhook contract with a
// valid lifecycle (draft|active|deprecated) is NOT flagged by FMT-01.
func TestFMT01_WebhookContractValidLifecycle(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["webhook.stripe.events.v1"] = &metadata.ContractMeta{
		ID:        "webhook.stripe.events.v1",
		Kind:      "webhook",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{
				PathPattern: "/webhooks/stripe",
				SourceID:    "stripe",
			},
		},
		File: "contracts/webhook/stripe/events/v1/contract.yaml",
	}

	results := NewValidator(project, "", clock.Real()).validateFMT01()
	for _, r := range results {
		if r.Code == codeFMT01 {
			t.Errorf("FMT-01: unexpected finding on valid webhook contract: %v", r)
		}
	}
}

// TestFMT01_WebhookContractInvalidLifecycle verifies that a webhook contract with
// an invalid lifecycle is flagged by FMT-01, proving the webhook kind participates
// in the same lifecycle checks as other kinds.
func TestFMT01_WebhookContractInvalidLifecycle(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["webhook.stripe.events.v1"] = &metadata.ContractMeta{
		ID:        "webhook.stripe.events.v1",
		Kind:      "webhook",
		Lifecycle: "unknown-invalid-lifecycle",
		File:      "contracts/webhook/stripe/events/v1/contract.yaml",
	}

	results := NewValidator(project, "", clock.Real()).validateFMT01()
	found := false
	for _, r := range results {
		if r.Code == codeFMT01 {
			found = true
		}
	}
	assert.True(t, found, "FMT-01 must flag a webhook contract with an invalid lifecycle")
}

// TestFMT09_WebhookKindIsValid verifies that a webhook contract does NOT trigger
// FMT-09 (invalid contract kind) after the impl adds "webhook" to validKinds.
func TestFMT09_WebhookKindIsValid(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["webhook.stripe.events.v1"] = &metadata.ContractMeta{
		ID:        "webhook.stripe.events.v1",
		Kind:      "webhook",
		Lifecycle: "active",
		OwnerCell: metadatatest.CellIDAccessCore,
		File:      "contracts/webhook/stripe/events/v1/contract.yaml",
	}
	project.Cells[metadatatest.CellIDAccessCore] = &metadata.CellMeta{
		ID:               metadatatest.CellIDAccessCore,
		Type:             "core",
		ConsistencyLevel: "L2",
		File:             "cells/accesscore/cell.yaml",
	}

	results := NewValidator(project, "", clock.Real()).validateFMT09()
	for _, r := range results {
		if r.Code == codeFMT09 {
			t.Errorf("FMT-09: webhook kind must not be flagged as invalid, got: %v", r)
		}
	}
}
