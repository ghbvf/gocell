package schemas

// projection_schema_test.go verifies that the new projection: and onReset:
// fields in slice.schema.json are correctly schema-validated:
//
//   - A subscribe CU with a valid projection passes.
//   - A subscribe CU with projection + onReset passes.
//   - A non-subscribe role (provide) carrying projection is rejected.
//   - A bad projectionID (uppercase, dash) is rejected by the pattern.
//   - A bad onReset (lowercase first letter) is rejected by the pattern.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjectionSliceCU_SubscribeWithProjectionPasses verifies that a valid
// subscribe CU carrying a snake_case projection id passes slice schema validation.
func TestProjectionSliceCU_SubscribeWithProjectionPasses(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "orderquery",
		"belongsToCell": "ordercell",
		"consistencyLevel": "L3",
		"contractUsages": [
			{
				"contract": "event.order.placed.v1",
				"role": "subscribe",
				"handler": "HandleOrderPlaced",
				"projection": "order_read_model"
			}
		],
		"verify": {
			"unit": ["unit.orderquery.handle"],
			"contract": ["contract.event.order.placed.v1.subscribe"]
		}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc),
		"subscribe CU with valid projection id must pass slice schema validation")
}

// TestProjectionSliceCU_SubscribeWithProjectionAndOnResetPasses verifies that a
// subscribe CU with both projection and onReset set passes slice schema validation.
func TestProjectionSliceCU_SubscribeWithProjectionAndOnResetPasses(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "orderquery",
		"belongsToCell": "ordercell",
		"consistencyLevel": "L3",
		"contractUsages": [
			{
				"contract": "event.order.placed.v1",
				"role": "subscribe",
				"handler": "HandleOrderPlaced",
				"projection": "order_read_model",
				"onReset": "ResetOrderModel"
			}
		],
		"verify": {
			"unit": ["unit.orderquery.handle"],
			"contract": ["contract.event.order.placed.v1.subscribe"]
		}
	}`), &doc))

	assert.NoError(t, schema.Validate(doc),
		"subscribe CU with projection and onReset must pass slice schema validation")
}

// TestProjectionSliceCU_NonSubscribeRoleWithProjectionFails verifies that a
// non-subscribe role (provide) carrying a projection field is rejected by the
// schema — projection is subscribe-only.
func TestProjectionSliceCU_NonSubscribeRoleWithProjectionFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "orderprovide",
		"belongsToCell": "ordercell",
		"consistencyLevel": "L0",
		"contractUsages": [
			{
				"contract": "data.order.read.v1",
				"role": "provide",
				"projection": "order_read_model"
			}
		],
		"verify": {
			"unit": [],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"non-subscribe role carrying projection must fail slice schema validation")
}

// TestProjectionSliceCU_NonSubscribeRoleWithOnResetFails verifies that a
// non-subscribe role carrying onReset is rejected by the schema.
func TestProjectionSliceCU_NonSubscribeRoleWithOnResetFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "orderprovide",
		"belongsToCell": "ordercell",
		"consistencyLevel": "L0",
		"contractUsages": [
			{
				"contract": "data.order.read.v1",
				"role": "provide",
				"onReset": "ResetModel"
			}
		],
		"verify": {
			"unit": [],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"non-subscribe role carrying onReset must fail slice schema validation")
}

// TestProjectionSliceCU_BadProjectionIDCapsAndDashFails verifies that a
// projectionID with uppercase letters or dashes fails the pattern constraint
// (^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$).
func TestProjectionSliceCU_BadProjectionIDCapsAndDashFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "orderquery",
		"belongsToCell": "ordercell",
		"consistencyLevel": "L3",
		"contractUsages": [
			{
				"contract": "event.order.placed.v1",
				"role": "subscribe",
				"handler": "HandleOrderPlaced",
				"projection": "Order-Status"
			}
		],
		"verify": {
			"unit": [],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"projectionID with uppercase/dash must fail slice schema pattern constraint")
}

// TestProjectionSliceCU_BadOnResetLowercaseFails verifies that an onReset value
// starting with a lowercase letter fails the exported-Go-identifier pattern
// (^[A-Z][A-Za-z0-9_]*$).
func TestProjectionSliceCU_BadOnResetLowercaseFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "orderquery",
		"belongsToCell": "ordercell",
		"consistencyLevel": "L3",
		"contractUsages": [
			{
				"contract": "event.order.placed.v1",
				"role": "subscribe",
				"handler": "HandleOrderPlaced",
				"projection": "order_read_model",
				"onReset": "resetProjection"
			}
		],
		"verify": {
			"unit": [],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"onReset starting with lowercase must fail slice schema pattern constraint")
}

// TestProjectionSliceCU_OnResetWithoutProjectionFails verifies that a subscribe
// CU with onReset but no projection is rejected by the schema (defense-in-depth
// parity with the Go parser validateProjectionUniqueness check).
func TestProjectionSliceCU_OnResetWithoutProjectionFails(t *testing.T) {
	schema := compileSliceSchema(t)

	var doc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "orderquery",
		"belongsToCell": "ordercell",
		"consistencyLevel": "L3",
		"contractUsages": [
			{
				"contract": "event.order.placed.v1",
				"role": "subscribe",
				"handler": "HandleOrderPlaced",
				"onReset": "ResetOrderModel"
			}
		],
		"verify": {
			"unit": [],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"subscribe CU with onReset but no projection must fail slice schema validation")
}

// TestProjectionSliceCU_WebhookReceiveWithProjectionFails verifies that a
// webhook-receive CU carrying projection is rejected (webhook-receive is a
// non-subscribe role for the purposes of the projection restriction).
func TestProjectionSliceCU_WebhookReceiveWithProjectionFails(t *testing.T) {
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
				"projection": "stripe_event_model"
			}
		],
		"verify": {
			"unit": [],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"webhook-receive role carrying projection must fail slice schema validation")
}

// TestProjectionSliceCU_WebhookDispatchWithOnResetFails verifies that a
// webhook-dispatch CU carrying onReset is rejected.
func TestProjectionSliceCU_WebhookDispatchWithOnResetFails(t *testing.T) {
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
				"sourceID": "shopify",
				"onReset": "ResetModel"
			}
		],
		"verify": {
			"unit": [],
			"contract": []
		}
	}`), &doc))

	assert.Error(t, schema.Validate(doc),
		"webhook-dispatch role carrying onReset must fail slice schema validation")
}
