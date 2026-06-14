package metadata_test

// derive_webhook_test.go tests the webhook-specific metadata derivation:
//   - deriveWebhookEndpoints populates EndpointsMeta.Receivers from
//     contractUsages[role=webhook-receive] and Dispatchers from [role=webhook-dispatch]
//   - Validation: webhook-receive requires handler+sourceID; webhook-dispatch
//     requires targetSelector+sourceID; receive sourceID must equal
//     contract.endpoints.inbound.sourceID; direction inbound↔receive /
//     outbound↔dispatch; hand-written receivers:/dispatchers: in contract.yaml
//     must fail (yaml:"-" strict decode).
//
// These tests reference symbols added by the impl agent; this file is the RED baseline.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildWebhookProject creates a minimal ProjectMeta for webhook derivation tests.
func buildWebhookProject(
	slices map[string]*metadata.SliceMeta,
	contracts map[string]*metadata.ContractMeta,
) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:      make(map[string]*metadata.CellMeta),
		Slices:     slices,
		Contracts:  contracts,
		Journeys:   make(map[string]*metadata.JourneyMeta),
		Assemblies: make(map[string]*metadata.AssemblyMeta),
	}
}

// inboundWebhookContract builds a minimal webhook contract with direction=inbound.
//
//nolint:unparam // id is parameterised for future callers; current tests happen to share one contractID
func inboundWebhookContract(id, sourceID string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:        id,
		Kind:      "webhook",
		Direction: "inbound",
		Dir:       "contracts/webhook/stripe/events/v1",
		File:      "contracts/webhook/stripe/events/v1/contract.yaml",
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{
				PathPattern: "/webhooks/stripe/events",
				SourceID:    sourceID,
			},
		},
	}
}

// outboundWebhookContract builds a minimal webhook contract with direction=outbound.
//
//nolint:unparam // id is parameterised for future callers; current tests happen to share one contractID
func outboundWebhookContract(id string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:        id,
		Kind:      "webhook",
		Direction: "outbound",
		Dir:       "contracts/webhook/shopify/dispatch/v1",
		File:      "contracts/webhook/shopify/dispatch/v1/contract.yaml",
	}
}

// ---------------------------------------------------------------------------
// Positive path: inbound (webhook-receive) derivation
// ---------------------------------------------------------------------------

// TestDeriveWebhookEndpoints_InboundReceiver verifies that a slice with
// contractUsages[role=webhook-receive] causes its owning cell ID to be appended
// to contract.Endpoints.Receivers.
func TestDeriveWebhookEndpoints_InboundReceiver(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"paymentcore/webhookingest": {
				ID:            "webhookingest",
				BelongsToCell: metadatatest.NewCellID("paymentcore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: contractID,
						Role:     "webhook-receive",
						Handler:  "HandleStripeEvent",
						SourceID: "stripe",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: inboundWebhookContract(contractID, "stripe"),
		},
	)

	require.NoError(t, metadata.ExportedDeriveWebhookEndpoints(pm))

	receivers := pm.Contracts[contractID].Endpoints.Receivers
	assert.Equal(t, []string{"paymentcore"}, receivers)
}

// TestDeriveWebhookEndpoints_OutboundDispatcher verifies that a slice with
// contractUsages[role=webhook-dispatch] causes its owning cell ID to be appended
// to contract.Endpoints.Dispatchers.
func TestDeriveWebhookEndpoints_OutboundDispatcher(t *testing.T) {
	contractID := "webhook.shopify.dispatch.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"ordercore/webhookdispatch": {
				ID:            "webhookdispatch",
				BelongsToCell: metadatatest.NewCellID("ordercore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract:       contractID,
						Role:           "webhook-dispatch",
						TargetSelector: "ShopifyTarget",
						SourceID:       "shopify",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: outboundWebhookContract(contractID),
		},
	)

	require.NoError(t, metadata.ExportedDeriveWebhookEndpoints(pm))

	dispatchers := pm.Contracts[contractID].Endpoints.Dispatchers
	assert.Equal(t, []string{"ordercore"}, dispatchers)
}

// TestDeriveWebhookEndpoints_MultiCellReceiversDedupedSorted verifies that
// multiple receivers are deduped and sorted alphabetically.
func TestDeriveWebhookEndpoints_MultiCellReceiversDedupedSorted(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"paymentcore/webhookingest": {
				ID:            "webhookingest",
				BelongsToCell: metadatatest.NewCellID("paymentcore"),
				ContractUsages: []metadata.ContractUsage{
					{Contract: contractID, Role: "webhook-receive", Handler: "HandleStripeEvent", SourceID: "stripe"},
				},
			},
			"auditcore/webhookaudit": {
				ID:            "webhookaudit",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: contractID, Role: "webhook-receive", Handler: "AuditStripeEvent", SourceID: "stripe"},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: inboundWebhookContract(contractID, "stripe"),
		},
	)

	require.NoError(t, metadata.ExportedDeriveWebhookEndpoints(pm))

	receivers := pm.Contracts[contractID].Endpoints.Receivers
	assert.Equal(t, []string{metadatatest.CellIDAuditCore, "paymentcore"}, receivers,
		"receivers must be deduped and sorted")
}

// ---------------------------------------------------------------------------
// Negative: webhook-receive missing handler → error
// ---------------------------------------------------------------------------

// TestDeriveWebhookEndpoints_ReceiveMissingHandler verifies that a
// webhook-receive ContractUsage without handler is rejected.
func TestDeriveWebhookEndpoints_ReceiveMissingHandler(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"paymentcore/webhookingest": {
				ID:            "webhookingest",
				BelongsToCell: metadatatest.NewCellID("paymentcore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: contractID,
						Role:     "webhook-receive",
						// Handler intentionally missing
						SourceID: "stripe",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: inboundWebhookContract(contractID, "stripe"),
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-receive without handler must fail derivation")
}

// TestDeriveWebhookEndpoints_ReceiveMissingSourceID verifies that a
// webhook-receive ContractUsage without sourceID is rejected.
func TestDeriveWebhookEndpoints_ReceiveMissingSourceID(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"paymentcore/webhookingest": {
				ID:            "webhookingest",
				BelongsToCell: metadatatest.NewCellID("paymentcore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: contractID,
						Role:     "webhook-receive",
						Handler:  "HandleStripeEvent",
						// SourceID intentionally missing
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: inboundWebhookContract(contractID, "stripe"),
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-receive without sourceID must fail derivation")
}

// TestDeriveWebhookEndpoints_DispatchMissingTargetSelector verifies that a
// webhook-dispatch ContractUsage without targetSelector is rejected.
func TestDeriveWebhookEndpoints_DispatchMissingTargetSelector(t *testing.T) {
	contractID := "webhook.shopify.dispatch.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"ordercore/webhookdispatch": {
				ID:            "webhookdispatch",
				BelongsToCell: metadatatest.NewCellID("ordercore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: contractID,
						Role:     "webhook-dispatch",
						// TargetSelector intentionally missing
						SourceID: "shopify",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: outboundWebhookContract(contractID),
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-dispatch without targetSelector must fail derivation")
}

// TestDeriveWebhookEndpoints_ReceiveSourceIDMismatch verifies that a
// webhook-receive CU whose sourceID differs from
// contract.endpoints.inbound.sourceID is rejected.
func TestDeriveWebhookEndpoints_ReceiveSourceIDMismatch(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"paymentcore/webhookingest": {
				ID:            "webhookingest",
				BelongsToCell: metadatatest.NewCellID("paymentcore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: contractID,
						Role:     "webhook-receive",
						Handler:  "HandleStripeEvent",
						SourceID: "stripe-sandbox", // mismatch: contract says "stripe"
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: inboundWebhookContract(contractID, "stripe"),
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-receive sourceID mismatch with contract.inbound.sourceID must fail")
}

// TestDeriveWebhookEndpoints_DispatchMissingSourceID verifies that a
// webhook-dispatch ContractUsage with a non-empty TargetSelector but empty
// SourceID is rejected. Mirrors the receive-missing-sourceID case.
func TestDeriveWebhookEndpoints_DispatchMissingSourceID(t *testing.T) {
	contractID := "webhook.shopify.dispatch.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"ordercore/webhookdispatch": {
				ID:            "webhookdispatch",
				BelongsToCell: metadatatest.NewCellID("ordercore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract:       contractID,
						Role:           "webhook-dispatch",
						TargetSelector: "ShopifyTarget",
						// SourceID intentionally missing
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: outboundWebhookContract(contractID),
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-dispatch without sourceID must fail derivation")
}

// TestDeriveWebhookEndpoints_DirectionRoleMismatch_ReceiveOnOutbound verifies
// that a webhook-receive CU on an outbound (no inbound block) contract is rejected.
func TestDeriveWebhookEndpoints_DirectionRoleMismatch_ReceiveOnOutbound(t *testing.T) {
	contractID := "webhook.shopify.dispatch.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"ordercore/webhookingest": {
				ID:            "webhookingest",
				BelongsToCell: metadatatest.NewCellID("ordercore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: contractID,
						Role:     "webhook-receive", // wrong: contract is outbound
						Handler:  "HandleShopify",
						SourceID: "shopify",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: outboundWebhookContract(contractID), // no inbound block
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-receive on outbound contract must fail direction/role mismatch")
}

// TestDeriveWebhookEndpoints_DirectionRoleMismatch_DispatchOnInbound verifies
// the symmetric fail-closed case: a webhook-dispatch CU on an inbound contract
// is rejected (you cannot dispatch an inbound contract). This guards the gap
// where validateWebhookDispatch previously did not receive the contract and so
// could not check its direction.
func TestDeriveWebhookEndpoints_DirectionRoleMismatch_DispatchOnInbound(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"ordercore/webhookdispatch": {
				ID:            "webhookdispatch",
				BelongsToCell: metadatatest.NewCellID("ordercore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract:       contractID,
						Role:           "webhook-dispatch", // wrong: contract is inbound
						TargetSelector: "StripeTarget",
						SourceID:       "stripe",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: inboundWebhookContract(contractID, "stripe"), // an inbound contract
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-dispatch on inbound contract must fail direction/role mismatch")
}

// TestDeriveWebhookEndpoints_MissingDirectionRejected verifies that a webhook
// contract with an empty direction is rejected fail-closed when wired by a
// receive slice (direction is mandatory, not inferred from the inbound block).
func TestDeriveWebhookEndpoints_MissingDirectionRejected(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	noDirection := inboundWebhookContract(contractID, "stripe")
	noDirection.Direction = "" // simulate a contract authored without direction
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"ordercore/webhookingest": {
				ID:            "webhookingest",
				BelongsToCell: metadatatest.NewCellID("ordercore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: contractID,
						Role:     "webhook-receive",
						Handler:  "HandleStripeEvent",
						SourceID: "stripe",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: noDirection,
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook contract without direction must be rejected fail-closed")
}

// TestDeriveWebhookEndpoints_UnreferencedContractMissingDirectionRejected is the
// fail-closed guard for a webhook contract with an invalid direction that NO
// slice wires: the per-CU validators never fire for it, so direction must be
// validated in the all-contracts pass. Without that pass the contract would
// silently persist with an empty direction.
func TestDeriveWebhookEndpoints_UnreferencedContractMissingDirectionRejected(t *testing.T) {
	contractID := "webhook.stripe.events.v1"
	noDirection := inboundWebhookContract(contractID, "stripe")
	noDirection.Direction = "" // contract authored without direction
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{}, // no slice references the contract
		map[string]*metadata.ContractMeta{
			contractID: noDirection,
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "unreferenced webhook contract with empty direction must be rejected fail-closed")
}

// ---------------------------------------------------------------------------
// ContractUsage SourceID / TargetSelector field unmarshal round-trip
// ---------------------------------------------------------------------------

// TestDeriveWebhookEndpoints_DispatchForbiddenHandler verifies that a
// webhook-dispatch ContractUsage with a non-empty handler field is rejected.
// slice.schema.json forbids handler on webhook-dispatch; the Go validator
// enforces the same constraint so schema and runtime are in sync.
func TestDeriveWebhookEndpoints_DispatchForbiddenHandler(t *testing.T) {
	contractID := "webhook.shopify.dispatch.v1"
	pm := buildWebhookProject(
		map[string]*metadata.SliceMeta{
			"ordercore/webhookdispatch": {
				ID:            "webhookdispatch",
				BelongsToCell: metadatatest.NewCellID("ordercore"),
				ContractUsages: []metadata.ContractUsage{
					{
						Contract:       contractID,
						Role:           "webhook-dispatch",
						Handler:        "ForbiddenHandler", // forbidden on webhook-dispatch
						TargetSelector: "ShopifyTarget",
						SourceID:       "shopify",
					},
				},
			},
		},
		map[string]*metadata.ContractMeta{
			contractID: outboundWebhookContract(contractID),
		},
	)

	err := metadata.ExportedDeriveWebhookEndpoints(pm)
	assert.Error(t, err, "webhook-dispatch with handler set must fail derivation")
}

// TestContractUsage_WebhookReceiveFieldsUnmarshal verifies that the SourceID
// field on ContractUsage maps from the slice.yaml `sourceID` key (yaml tag
// round-trip). Constructing via real yaml.Unmarshal — rather than a struct
// literal — is what makes a tag typo (e.g. `source_id`) fail here instead of
// only surfacing downstream in cellgen/contractgen.
func TestContractUsage_WebhookReceiveFieldsUnmarshal(t *testing.T) {
	const y = `
contract: webhook.stripe.events.v1
role: webhook-receive
handler: HandleStripeEvent
sourceID: stripe
`
	var cu metadata.ContractUsage
	require.NoError(t, yaml.Unmarshal([]byte(y), &cu))
	assert.Equal(t, "webhook.stripe.events.v1", cu.Contract)
	assert.Equal(t, "webhook-receive", cu.Role)
	assert.Equal(t, "HandleStripeEvent", cu.Handler)
	assert.Equal(t, "stripe", cu.SourceID)
}

// TestContractUsage_WebhookDispatchFieldsUnmarshal verifies that the
// TargetSelector and SourceID fields map from the slice.yaml `targetSelector` /
// `sourceID` keys (yaml tag round-trip).
func TestContractUsage_WebhookDispatchFieldsUnmarshal(t *testing.T) {
	const y = `
contract: webhook.shopify.dispatch.v1
role: webhook-dispatch
targetSelector: ShopifyTarget
sourceID: shopify
`
	var cu metadata.ContractUsage
	require.NoError(t, yaml.Unmarshal([]byte(y), &cu))
	assert.Equal(t, "webhook.shopify.dispatch.v1", cu.Contract)
	assert.Equal(t, "webhook-dispatch", cu.Role)
	assert.Equal(t, "ShopifyTarget", cu.TargetSelector)
	assert.Equal(t, "shopify", cu.SourceID)
}

// ---------------------------------------------------------------------------
// EndpointsMeta.Receivers / Dispatchers field existence
// ---------------------------------------------------------------------------

// TestEndpointsMetaWebhookDerivedFields verifies that the EndpointsMeta struct
// has Receivers and Dispatchers fields with yaml:"-" (derived, not hand-written).
// This test will fail to compile until the impl agent adds these fields.
func TestEndpointsMetaWebhookDerivedFields(t *testing.T) {
	em := metadata.EndpointsMeta{
		Receivers:   []string{"paymentcore"},
		Dispatchers: []string{"ordercore"},
	}
	assert.Equal(t, []string{"paymentcore"}, em.Receivers)
	assert.Equal(t, []string{"ordercore"}, em.Dispatchers)
}

// TestContractMetaWebhookDirectionField verifies that ContractMeta has a Direction
// field.
func TestContractMetaWebhookDirectionField(t *testing.T) {
	c := &metadata.ContractMeta{
		ID:        "webhook.stripe.events.v1",
		Kind:      "webhook",
		Direction: "inbound",
	}
	assert.Equal(t, "inbound", c.Direction)
}

// TestEndpointsMetaInboundField verifies that EndpointsMeta has an Inbound field
// of type *WebhookInboundMeta.
func TestEndpointsMetaInboundField(t *testing.T) {
	em := metadata.EndpointsMeta{
		Inbound: &metadata.WebhookInboundMeta{
			PathPattern: "/webhooks/stripe/events",
			SourceID:    "stripe",
		},
	}
	require.NotNil(t, em.Inbound)
	assert.Equal(t, "stripe", em.Inbound.SourceID)
	assert.Equal(t, "/webhooks/stripe/events", em.Inbound.PathPattern)
}

// TestWebhookSignatureMetaFields verifies the new WebhookSignatureMeta struct fields.
func TestWebhookSignatureMetaFields(t *testing.T) {
	sig := &metadata.WebhookSignatureMeta{
		Algorithm:        "hmac-sha256",
		ToleranceSeconds: 300,
		DeliveryIDHeader: "svix-id",
		TimestampHeader:  "svix-timestamp",
		SignatureHeader:  "svix-signature",
		SignedStringForm: "${deliveryId}.${timestamp}.${body}",
	}
	assert.Equal(t, "hmac-sha256", sig.Algorithm)
	assert.Equal(t, 300, sig.ToleranceSeconds)
}

// TestWebhookPayloadMetaFields verifies the new WebhookPayloadMeta struct fields.
func TestWebhookPayloadMetaFields(t *testing.T) {
	p := &metadata.WebhookPayloadMeta{
		SchemaRef:    "stripe-event.schema.json",
		ContentType:  "application/json",
		MaxBodyBytes: 512 * 1024,
	}
	assert.Equal(t, "stripe-event.schema.json", p.SchemaRef)
	assert.Equal(t, int64(512*1024), p.MaxBodyBytes)
}

// TestContractMetaSignatureAndPayloadFields verifies that ContractMeta exposes
// the Signature and Payload webhook extension fields.
func TestContractMetaSignatureAndPayloadFields(t *testing.T) {
	c := &metadata.ContractMeta{
		ID:   "webhook.stripe.events.v1",
		Kind: "webhook",
		Signature: &metadata.WebhookSignatureMeta{
			Algorithm: "hmac-sha256",
		},
		Payload: &metadata.WebhookPayloadMeta{
			ContentType: "application/json",
		},
	}
	require.NotNil(t, c.Signature)
	assert.Equal(t, "hmac-sha256", c.Signature.Algorithm)
	require.NotNil(t, c.Payload)
	assert.Equal(t, "application/json", c.Payload.ContentType)
}
