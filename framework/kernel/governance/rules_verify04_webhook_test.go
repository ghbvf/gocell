package governance

// rules_verify04_webhook_test.go covers VERIFY-04 for the webhook contract kind.
//
// VERIFY-04 ("an active contract whose provider is a cell must have an
// implementing slice in that cell") was written before any live webhook cell
// existed, so it only recognized provider-role slices. An INBOUND webhook is
// special: its provider (ProviderEndpoint) is the owner cell, but the owner
// implements it via a webhook-receive slice — which cellvocab classifies as a
// CONSUMER role (the cell consumes the inbound delivery; the real "provider" is
// the external sender). hasImplementingSlice/implementsContract close that gap:
// an inbound webhook is implemented by webhook-receive; everything else
// (including outbound webhook via webhook-dispatch) by a provider role.

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// webhookContractOwnedByDemo builds an active webhook contract owned by the demo
// cell (the cell sliceWithCU's slices belong to), in the given direction.
func webhookContractOwnedByDemo(id, direction string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:        id,
		Kind:      "webhook",
		Direction: direction,
		Lifecycle: "active",
		OwnerCell: metadatatest.CellIDDemo,
		File:      "contracts/webhook/demo/events/v1/contract.yaml",
	}
}

// demoCellL1 registers the demo owner cell so VERIFY-04 treats the contract's
// provider as a cell (not an external actor).
func demoCellL1(project *metadata.ProjectMeta) {
	project.Cells[metadatatest.CellIDDemo] = &metadata.CellMeta{
		ID:               metadatatest.CellIDDemo,
		Type:             "edge",
		ConsistencyLevel: "L1",
		File:             "cells/demo/cell.yaml",
	}
}

// TestVERIFY04_InboundWebhook_ReceiveSliceSatisfies verifies that a
// webhook-receive slice in the owner cell satisfies VERIFY-04 for an inbound
// webhook contract (the gap this fix closes — webhook-receive is a consumer role
// but is the inbound contract's implementer).
func TestVERIFY04_InboundWebhook_ReceiveSliceSatisfies(t *testing.T) {
	t.Parallel()
	project := minimalGovernanceProject()
	demoCellL1(project)
	project.Contracts["webhook.demo.events.v1"] = webhookContractOwnedByDemo("webhook.demo.events.v1", "inbound")
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.demo.events.v1", Role: "webhook-receive",
		Handler: "HandleEvent", SourceID: "demosource",
	})

	results := NewValidator(project, "", clock.Real()).validateVERIFY04()
	assertNoCode(t, results, codeVERIFY04)
}

// TestVERIFY04_InboundWebhook_NoReceiveSlice_Rejected verifies the ghost-capability
// guard still holds for webhooks: an active inbound webhook contract with no
// webhook-receive slice in its owner cell is flagged.
func TestVERIFY04_InboundWebhook_NoReceiveSlice_Rejected(t *testing.T) {
	t.Parallel()
	project := minimalGovernanceProject()
	demoCellL1(project)
	project.Contracts["webhook.demo.events.v1"] = webhookContractOwnedByDemo("webhook.demo.events.v1", "inbound")
	// No webhook-receive slice → nobody receives the declared inbound webhook.

	results := NewValidator(project, "", clock.Real()).validateVERIFY04()
	assertResultsContainCode(t, results, codeVERIFY04, "lifecycle")
}

// TestVERIFY04_InboundWebhook_OnlyDispatchSlice_Rejected verifies that a
// webhook-dispatch slice (the OUTBOUND role) does not satisfy an INBOUND webhook
// contract — the directions are distinct implementing roles.
func TestVERIFY04_InboundWebhook_OnlyDispatchSlice_Rejected(t *testing.T) {
	t.Parallel()
	project := minimalGovernanceProject()
	demoCellL1(project)
	project.Contracts["webhook.demo.events.v1"] = webhookContractOwnedByDemo("webhook.demo.events.v1", "inbound")
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.demo.events.v1", Role: "webhook-dispatch",
		TargetSelector: "Target", SourceID: "demosource",
	})

	results := NewValidator(project, "", clock.Real()).validateVERIFY04()
	assertResultsContainCode(t, results, codeVERIFY04, "lifecycle")
}

// TestVERIFY04_OutboundWebhook_DispatchSliceSatisfies verifies the unchanged
// outbound path: webhook-dispatch is a provider role, so it satisfies VERIFY-04
// for an outbound webhook contract.
func TestVERIFY04_OutboundWebhook_DispatchSliceSatisfies(t *testing.T) {
	t.Parallel()
	project := minimalGovernanceProject()
	demoCellL1(project)
	project.Contracts["webhook.shopify.dispatch.v1"] = webhookContractOwnedByDemo("webhook.shopify.dispatch.v1", "outbound")
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.shopify.dispatch.v1", Role: "webhook-dispatch",
		TargetSelector: "ShopifyTarget", SourceID: "shopify",
	})

	results := NewValidator(project, "", clock.Real()).validateVERIFY04()
	assertNoCode(t, results, codeVERIFY04)
}
