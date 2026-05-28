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
			"signedStringForm": "${deliveryId}.${timestamp}.${body}"
		},
		"payload": {
			"contentType": "application/json",
			"maxBodyBytes": 524288
		}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc), "valid webhook inbound contract must pass schema validation")
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
			"contract": ["contract.webhook.stripe.events.v1.receive"]
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
			"contract": ["contract.webhook.shopify.dispatch.v1.dispatch"]
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
