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
	"github.com/ghbvf/gocell/kernel/webhook"
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
		File:             "corecells/accesscore/cell.yaml",
	}

	results := NewValidator(project, "", clock.Real()).validateFMT09()
	for _, r := range results {
		if r.Code == codeFMT09 {
			t.Errorf("FMT-09: webhook kind must not be flagged as invalid, got: %v", r)
		}
	}
}

// --- FMT-35 per-role placement matrix (webhook-receive / webhook-dispatch) ---
//
// These guard the contradiction where cellgen + slice.schema.json require
// handler/sourceID/targetSelector on the webhook roles while the FMT-35
// governance rule (the live enforcement path) previously forbade them on any
// non-subscribe role. The matrix in validateFMT35 must stay in sync with
// slice.schema.json.

// TestFMT35_WebhookReceive_Valid_Passes verifies a well-formed webhook-receive
// CU (handler + sourceID, optional field) produces no FMT-35 finding.
func TestFMT35_WebhookReceive_Valid_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.stripe.events.v1", Role: "webhook-receive",
		Handler: "HandleStripeEvent", SourceID: "stripe", Field: "stripeIngest",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertNoCode(t, results, codeFMT35)
}

// TestFMT35_WebhookReceive_MissingHandler_Rejected verifies handler is required.
func TestFMT35_WebhookReceive_MissingHandler_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.stripe.events.v1", Role: "webhook-receive", SourceID: "stripe",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].handler")
}

// TestFMT35_WebhookReceive_MissingSourceID_Rejected verifies sourceID is required.
func TestFMT35_WebhookReceive_MissingSourceID_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.stripe.events.v1", Role: "webhook-receive", Handler: "HandleStripeEvent",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].sourceID")
}

// TestFMT35_WebhookReceive_WithGroupAndTargetSelector_Rejected verifies group and
// targetSelector are forbidden on webhook-receive.
func TestFMT35_WebhookReceive_WithGroupAndTargetSelector_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.stripe.events.v1", Role: "webhook-receive",
		Handler: "HandleStripeEvent", SourceID: "stripe", Group: "g", TargetSelector: "T",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].group")
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].targetSelector")
}

// TestFMT35_WebhookDispatch_Valid_Passes verifies a well-formed webhook-dispatch
// CU (targetSelector + sourceID) produces no FMT-35 finding.
func TestFMT35_WebhookDispatch_Valid_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.shopify.dispatch.v1", Role: "webhook-dispatch",
		TargetSelector: "ShopifyTarget", SourceID: "shopify",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertNoCode(t, results, codeFMT35)
}

// TestFMT35_WebhookDispatch_MissingTargetSelector_Rejected verifies targetSelector
// is required for webhook-dispatch.
func TestFMT35_WebhookDispatch_MissingTargetSelector_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.shopify.dispatch.v1", Role: "webhook-dispatch", SourceID: "shopify",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].targetSelector")
}

// TestFMT35_WebhookDispatch_WithHandler_Rejected verifies handler is forbidden on
// webhook-dispatch (use targetSelector instead).
func TestFMT35_WebhookDispatch_WithHandler_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.shopify.dispatch.v1", Role: "webhook-dispatch",
		TargetSelector: "ShopifyTarget", SourceID: "shopify", Handler: "Nope",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].handler")
}

// TestFMT35_WebhookDispatch_MissingSourceID_Rejected verifies sourceID is
// required for webhook-dispatch (completes the dispatch required-column matrix).
func TestFMT35_WebhookDispatch_MissingSourceID_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "webhook.shopify.dispatch.v1", Role: "webhook-dispatch", TargetSelector: "ShopifyTarget",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].sourceID")
}

// TestFMT35_SubscribeWithSourceID_Rejected verifies sourceID (a webhook-only
// column) is forbidden on subscribe.
func TestFMT35_SubscribeWithSourceID_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "event.foo.v1", Role: "subscribe", Handler: "HandleFoo", SourceID: "stripe",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].sourceID")
}

// TestFMT35_NonWebhookRole_SourceIDAndTargetSelectorForbidden verifies the two
// webhook-only columns are forbidden on ordinary non-subscribe roles (the
// matrix's default branch forbids all five columns).
func TestFMT35_NonWebhookRole_SourceIDAndTargetSelectorForbidden(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/srv"] = sliceWithCU(metadata.ContractUsage{
		Contract: "http.users.v1", Role: "serve", SourceID: "stripe", TargetSelector: "T",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].sourceID")
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].targetSelector")
}

// --- FMT-38 webhook contract-side required fields (live parity with FMT-04) ---
//
// These guard the fail-open gap where an inbound webhook contract that omits
// signature/payload, or declares an unsupported signature algorithm, would pass
// `gocell validate` (the schema if/then encoding the rule is not run — Phase 2),
// unlike event contracts whose required fields are live-enforced by FMT-04.

// TestFMT38AlgorithmMatchesKernel is the single-source cross-check: the FMT-38
// local algorithm literal MUST equal the runtime kernel/webhook canonical const.
// kernel/governance cannot import kernel/webhook in production (KERNEL-INTERNAL-DAG-01
// forbids the governance→webhook edge), so the value is duplicated as a local
// literal and pinned here. This _test.go import of kernel/webhook is test-only
// and therefore not counted as a production DAG edge (loadModule uses tests=false).
func TestFMT38AlgorithmMatchesKernel(t *testing.T) {
	assert.Equal(t, string(webhook.AlgorithmHMACSHA256), fmt38WebhookAlgorithm,
		"FMT-38 local algorithm literal must stay in lock-step with kernel/webhook.AlgorithmHMACSHA256")
}

// inboundWebhookContractMeta builds a well-formed inbound webhook ContractMeta
// (signature with the sole supported algorithm + payload) for FMT-38 tests.
func inboundWebhookContractMeta() *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:        "webhook.stripe.events.v1",
		Kind:      string(cellvocab.ContractWebhook),
		Lifecycle: "active",
		Direction: "inbound",
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{PathPattern: "/webhooks/stripe", SourceID: "stripe"},
		},
		Signature: &metadata.WebhookSignatureMeta{
			Algorithm:        fmt38WebhookAlgorithm,
			ToleranceSeconds: 300,
			DeliveryIDHeader: "svix-id",
			TimestampHeader:  "svix-timestamp",
			SignatureHeader:  "svix-signature",
			SignedStringForm: "{deliveryID}.{timestamp}.{body}",
		},
		Payload: &metadata.WebhookPayloadMeta{ContentType: "application/json", MaxBodyBytes: 524288},
		File:    "contracts/webhook/stripe/events/v1/contract.yaml",
	}
}

// TestFMT38_InboundValid_Passes verifies a well-formed inbound webhook contract
// (signature with hmac-sha256 + payload) produces no FMT-38 finding.
func TestFMT38_InboundValid_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["webhook.stripe.events.v1"] = inboundWebhookContractMeta()

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertNoCode(t, results, codeFMT38)
}

// TestFMT38_InboundMissingSignature_Rejected verifies signature is required for
// inbound (a receiver cannot verify without it).
func TestFMT38_InboundMissingSignature_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Signature = nil
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature")
}

// TestFMT38_InboundMissingPayload_Rejected verifies payload is required for inbound.
func TestFMT38_InboundMissingPayload_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Payload = nil
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "payload")
}

// TestFMT38_InvalidAlgorithm_Rejected verifies a signature algorithm other than
// hmac-sha256 is rejected (no downgrade path).
func TestFMT38_InvalidAlgorithm_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Signature.Algorithm = "hmac-sha1"
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature.algorithm")
}

// TestFMT38_InboundZeroTolerance_Rejected verifies that an inbound webhook whose
// signature.toleranceSeconds is 0 (disables the replay-attack window, fail-open)
// is rejected — parity with the runtime HMAC verifier requiring a positive tolerance.
func TestFMT38_InboundZeroTolerance_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Signature.ToleranceSeconds = 0
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature.toleranceSeconds")
}

// TestFMT38_InboundZeroMaxBody_Rejected verifies that an inbound webhook whose
// payload.maxBodyBytes is 0 (unbounded request body, DoS surface) is rejected.
func TestFMT38_InboundZeroMaxBody_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Payload.MaxBodyBytes = 0
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "payload.maxBodyBytes")
}

// TestFMT38_InboundEmptyDeliveryIDHeader_Rejected verifies that an inbound
// webhook whose signature block is present but carries an empty
// deliveryIDHeader is rejected — the runtime HMAC verifier cannot build the
// signed string without it (a partially-hollow shell is fail-open).
func TestFMT38_InboundEmptyDeliveryIDHeader_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Signature.DeliveryIDHeader = ""
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature.deliveryIDHeader")
}

// TestFMT38_InboundEmptyTimestampHeader_Rejected verifies that an inbound
// webhook signature block with an empty timestampHeader is rejected.
func TestFMT38_InboundEmptyTimestampHeader_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Signature.TimestampHeader = ""
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature.timestampHeader")
}

// TestFMT38_InboundEmptySignatureHeader_Rejected verifies that an inbound
// webhook signature block with an empty signatureHeader is rejected.
func TestFMT38_InboundEmptySignatureHeader_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Signature.SignatureHeader = ""
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature.signatureHeader")
}

// TestFMT38_InboundEmptySignedStringForm_Rejected verifies that an inbound
// webhook signature block with an empty signedStringForm template is rejected.
func TestFMT38_InboundEmptySignedStringForm_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Signature.SignedStringForm = ""
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature.signedStringForm")
}

// TestFMT38_InboundEmptyContentType_Rejected verifies that an inbound webhook
// whose payload block is present but carries an empty contentType is rejected —
// the receiver cannot enforce a Content-Type without it.
func TestFMT38_InboundEmptyContentType_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	c := inboundWebhookContractMeta()
	c.Payload.ContentType = ""
	project.Contracts["webhook.stripe.events.v1"] = c

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "payload.contentType")
}

// TestFMT38_OutboundWithoutSignaturePayload_Passes verifies outbound webhooks do
// NOT require signature/payload (the dispatcher signs with its own key).
func TestFMT38_OutboundWithoutSignaturePayload_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["webhook.shopify.dispatch.v1"] = &metadata.ContractMeta{
		ID:        "webhook.shopify.dispatch.v1",
		Kind:      string(cellvocab.ContractWebhook),
		Lifecycle: "active",
		Direction: "outbound",
		File:      "contracts/webhook/shopify/dispatch/v1/contract.yaml",
	}

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertNoCode(t, results, codeFMT38)
}

// TestFMT38_OutboundWithBadAlgorithm_Rejected verifies the algorithm check
// applies whenever a signature block is present, including on outbound contracts.
func TestFMT38_OutboundWithBadAlgorithm_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["webhook.shopify.dispatch.v1"] = &metadata.ContractMeta{
		ID:        "webhook.shopify.dispatch.v1",
		Kind:      string(cellvocab.ContractWebhook),
		Lifecycle: "active",
		Direction: "outbound",
		Signature: &metadata.WebhookSignatureMeta{Algorithm: "md5"},
		File:      "contracts/webhook/shopify/dispatch/v1/contract.yaml",
	}

	results := NewValidator(project, "", clock.Real()).validateFMT38()
	assertResultsContainCode(t, results, codeFMT38, "signature.algorithm")
}
