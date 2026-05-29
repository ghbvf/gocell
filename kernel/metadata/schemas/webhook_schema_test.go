package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebhookContractSchemaValid verifies that a well-formed webhook contract
// passes schema validation.
func TestWebhookContractSchemaValid(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc), "valid webhook inbound contract must pass schema validation")
}

// TestWebhookContractSchema_MissingDirectionFails verifies that a webhook
// contract that omits the direction field is rejected (direction is required so
// the inbound/outbound flow is never ambiguous — fail-closed).
func TestWebhookContractSchema_MissingDirectionFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"webhook contract without direction must be rejected (direction is required)")
}

// TestWebhookContractSchema_InboundMissingSignatureFails verifies that an
// inbound webhook contract that omits signature is rejected (inbound requires
// signature + payload so verification metadata is never absent — fail-closed).
func TestWebhookContractSchema_InboundMissingSignatureFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook contract without signature must be rejected (signature is required for inbound)")
}

// TestWebhookContractSchema_InvalidAlgorithmFails verifies that a webhook
// signature declaring an algorithm other than hmac-sha256 is rejected by the
// enum constraint (aligns with runtime kernel/webhook AlgorithmHMACSHA256, the
// sole supported value; no downgrade path to weaker MACs).
func TestWebhookContractSchema_InvalidAlgorithmFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha1",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"webhook signature with algorithm other than hmac-sha256 must be rejected by the enum constraint")
}

// TestWebhookContractSchema_OutboundValid verifies that a well-formed outbound
// webhook contract — which carries no inbound block and (optionally) no
// signature/payload, since the dispatcher signs with its own key — passes
// schema validation. Asserts the inbound-only required constraints do not leak
// onto the outbound direction.
func TestWebhookContractSchema_OutboundValid(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.shopify.orders.v1",
		"kind": "webhook",
		"ownerCell": "ordercore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "outbound",
		"endpoints": {}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc),
		"valid outbound webhook contract (no inbound/signature/payload) must pass schema validation")
}

// TestWebhookContractSchema_InboundMissingPayloadFails verifies the symmetric
// counterpart of the missing-signature case: an inbound webhook that omits the
// payload block is rejected (the receiver needs payload constraints).
func TestWebhookContractSchema_InboundMissingPayloadFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook contract without payload must be rejected (payload is required for inbound)")
}

// TestWebhookContractSchema_ZeroToleranceFails verifies signature.toleranceSeconds
// of 0 is rejected by the minimum:1 constraint — 0 disables the replay-attack
// window (fail-open). Parity with FMT-38 and the runtime HMAC verifier.
func TestWebhookContractSchema_ZeroToleranceFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 0,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"signature.toleranceSeconds=0 must be rejected by minimum:1 (disabling the replay window is fail-open)")
}

// TestWebhookContractSchema_ZeroMaxBodyFails verifies payload.maxBodyBytes of 0
// is rejected by the minimum:1 constraint — 0 means an unbounded request body
// (DoS surface). Parity with FMT-38.
func TestWebhookContractSchema_ZeroMaxBodyFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 0
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"payload.maxBodyBytes=0 must be rejected by minimum:1 (unbounded body is a DoS surface)")
}

// TestWebhookContractSchema_InboundMissingDeliveryIDHeaderFails verifies that an
// inbound webhook signature block omitting deliveryIDHeader is rejected — every
// signed-string ingredient is required on inbound so the runtime HMAC verifier
// can build the signed string (fail-closed at declaration).
func TestWebhookContractSchema_InboundMissingDeliveryIDHeaderFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook signature without deliveryIDHeader must be rejected (required for inbound)")
}

// TestWebhookContractSchema_InboundMissingTimestampHeaderFails verifies that an
// inbound webhook signature block omitting timestampHeader is rejected.
func TestWebhookContractSchema_InboundMissingTimestampHeaderFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook signature without timestampHeader must be rejected (required for inbound)")
}

// TestWebhookContractSchema_InboundMissingSignatureHeaderFails verifies that an
// inbound webhook signature block omitting signatureHeader is rejected.
func TestWebhookContractSchema_InboundMissingSignatureHeaderFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook signature without signatureHeader must be rejected (required for inbound)")
}

// TestWebhookContractSchema_InboundMissingSignedStringFormFails verifies that an
// inbound webhook signature block omitting signedStringForm is rejected.
func TestWebhookContractSchema_InboundMissingSignedStringFormFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook signature without signedStringForm must be rejected (required for inbound)")
}

// TestWebhookContractSchema_InboundEmptyDeliveryIDHeaderFails verifies that an
// inbound webhook signature block with an empty-string deliveryIDHeader is
// rejected by the minLength:1 constraint (a hollow shell is fail-open).
func TestWebhookContractSchema_InboundEmptyDeliveryIDHeaderFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook signature with empty deliveryIDHeader must be rejected by minLength:1")
}

// TestWebhookContractSchema_InboundMissingContentTypeFails verifies that an
// inbound webhook payload block omitting contentType is rejected — the receiver
// cannot enforce a Content-Type on incoming deliveries without it.
func TestWebhookContractSchema_InboundMissingContentTypeFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook payload without contentType must be rejected (required for inbound)")
}

// TestWebhookContractSchema_InboundEmptyContentTypeFails verifies that an
// inbound webhook payload block with an empty-string contentType is rejected by
// the minLength:1 constraint.
func TestWebhookContractSchema_InboundEmptyContentTypeFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300,
			"deliveryIDHeader": "svix-id",
			"timestampHeader": "svix-timestamp",
			"signatureHeader": "svix-signature",
			"signedStringForm": "{deliveryID}.{timestamp}.{body}"
		},
		"payload": {
			"contentType": "",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"inbound webhook payload with empty contentType must be rejected by minLength:1")
}

// TestWebhookContractSchema_PartiallyHollowShellFails is the regression test for
// the verified gap: an inbound contract whose signature block carries only
// algorithm+toleranceSeconds and whose payload block carries only maxBodyBytes
// (all headers / signedStringForm / contentType empty) was previously accepted
// by both the schema and FMT-38. It must now be rejected at the schema layer.
func TestWebhookContractSchema_PartiallyHollowShellFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"direction": "inbound",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			}
		},
		"signature": {
			"algorithm": "hmac-sha256",
			"toleranceSeconds": 300
		},
		"payload": {
			"maxBodyBytes": 1048576
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"partially-hollow inbound shell (no headers/signedStringForm/contentType) must be rejected by the inbound required constraints")
}

// TestWebhookContractSchema_HandWrittenReceiversFails verifies that a
// contract.yaml with a hand-written "receivers" key under endpoints is
// rejected by additionalProperties:false (receivers is derived, yaml:"-").
func TestWebhookContractSchema_HandWrittenReceiversFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.stripe.events.v1",
		"kind": "webhook",
		"ownerCell": "paymentcore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"inbound": {
				"pathPattern": "/webhooks/stripe/events",
				"sourceID": "stripe"
			},
			"receivers": ["paymentcore"]
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"hand-written receivers: in webhook endpoints must be rejected by additionalProperties:false")
}

// TestWebhookContractSchema_HandWrittenDispatchersFails verifies that a
// contract.yaml with a hand-written "dispatchers" key under endpoints is
// rejected.
func TestWebhookContractSchema_HandWrittenDispatchersFails(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhook.shopify.dispatch.v1",
		"kind": "webhook",
		"ownerCell": "ordercore",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"dispatchers": ["ordercore"]
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"hand-written dispatchers: in webhook endpoints must be rejected by additionalProperties:false")
}

// TestWebhookReceiveSliceCUValid verifies that a well-formed webhook-receive
// contractUsage passes slice schema validation.
func TestWebhookReceiveSliceCUValid(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhookingest",
		"belongsToCell": "paymentcore",
		"consistencyLevel": "L1",
		"contractUsages": [
			{
				"contract": "webhook.stripe.events.v1",
				"role": "webhook-receive",
				"handler": "HandleStripeEvent",
				"sourceID": "stripe"
			}
		],
		"verify": {
			"unit": ["unit.webhookingest.handle"],
			"contract": ["contract.webhook.stripe.events.v1.webhook-receive"]
		}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc), "valid webhook-receive contractUsage must pass slice schema")
}

// TestWebhookDispatchSliceCUValid verifies that a well-formed webhook-dispatch
// contractUsage passes slice schema validation.
func TestWebhookDispatchSliceCUValid(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhookdispatch",
		"belongsToCell": "ordercore",
		"consistencyLevel": "L1",
		"contractUsages": [
			{
				"contract": "webhook.shopify.dispatch.v1",
				"role": "webhook-dispatch",
				"targetSelector": "ShopifyTarget",
				"sourceID": "shopify"
			}
		],
		"verify": {
			"unit": ["unit.webhookdispatch.dispatch"],
			"contract": ["contract.webhook.shopify.dispatch.v1.webhook-dispatch"]
		}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc), "valid webhook-dispatch contractUsage must pass slice schema")
}

// TestWebhookReceiveSliceCU_MissingSourceIDFails verifies that a
// webhook-receive contractUsage without sourceID is rejected by the schema.
func TestWebhookReceiveSliceCU_MissingSourceIDFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhookingest",
		"belongsToCell": "paymentcore",
		"consistencyLevel": "L1",
		"contractUsages": [
			{
				"contract": "webhook.stripe.events.v1",
				"role": "webhook-receive",
				"handler": "HandleStripeEvent"
			}
		],
		"verify": {
			"unit": ["unit.webhookingest.handle"],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"webhook-receive contractUsage without sourceID must fail schema validation")
}

// TestWebhookDispatchSliceCU_MissingTargetSelectorFails verifies that a
// webhook-dispatch contractUsage without targetSelector is rejected by the schema.
func TestWebhookDispatchSliceCU_MissingTargetSelectorFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhookdispatch",
		"belongsToCell": "ordercore",
		"consistencyLevel": "L1",
		"contractUsages": [
			{
				"contract": "webhook.shopify.dispatch.v1",
				"role": "webhook-dispatch",
				"sourceID": "shopify"
			}
		],
		"verify": {
			"unit": ["unit.webhookdispatch.dispatch"],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"webhook-dispatch contractUsage without targetSelector must fail schema validation")
}

// TestWebhookReceiveSliceCU_WithFieldPasses verifies that a webhook-receive
// contractUsage WITH a field value passes slice schema validation. Field is
// optional for webhook-receive (cellgen uses it for disambiguation).
func TestWebhookReceiveSliceCU_WithFieldPasses(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhookingest",
		"belongsToCell": "paymentcore",
		"consistencyLevel": "L1",
		"contractUsages": [
			{
				"contract": "webhook.stripe.events.v1",
				"role": "webhook-receive",
				"handler": "HandleStripeEvent",
				"sourceID": "stripe",
				"field": "webhookIngest"
			}
		],
		"verify": {
			"unit": ["unit.webhookingest.handle"],
			"contract": ["contract.webhook.stripe.events.v1.webhook-receive"]
		}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc),
		"webhook-receive contractUsage with field must pass slice schema (field is optional for webhook-receive)")
}

// TestWebhookReceiveSliceCU_InvalidSourceIDFails verifies that a webhook-receive
// contractUsage with an invalid sourceID (uppercase, spaces, etc.) is rejected
// by the slice schema pattern constraint.
func TestWebhookReceiveSliceCU_InvalidSourceIDFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "webhookingest",
		"belongsToCell": "paymentcore",
		"consistencyLevel": "L1",
		"contractUsages": [
			{
				"contract": "webhook.stripe.events.v1",
				"role": "webhook-receive",
				"handler": "HandleStripeEvent",
				"sourceID": "STRIPE_INVALID"
			}
		],
		"verify": {
			"unit": ["unit.webhookingest.handle"],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"webhook-receive contractUsage with uppercase sourceID must fail schema validation (pattern ^[a-z][a-z0-9_-]{0,63}$)")
}

// TestNonWebhookRole_SourceIDForbidden verifies that a non-webhook role
// carrying sourceID is rejected by the slice schema.
func TestNonWebhookRole_SourceIDForbidden(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "serveslice",
		"belongsToCell": "paymentcore",
		"consistencyLevel": "L1",
		"contractUsages": [
			{
				"contract": "http.payment.process.v1",
				"role": "serve",
				"sourceID": "stripe"
			}
		],
		"verify": {
			"unit": ["unit.serveslice.serve"],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"non-webhook role carrying sourceID must fail slice schema validation")
}
