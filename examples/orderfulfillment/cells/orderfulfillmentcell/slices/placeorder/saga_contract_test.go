package placeorder_test

// saga_contract_test.go verifies that the registered saga definition matches
// the contract declared in contracts/saga/orderfulfillment/v1/contract.yaml.
// This satisfies the slice.yaml verify.contract entry:
//   contract.saga.orderfulfillment.v1.orchestrate
//
// Test name TestSagaOrderfulfillmentV1Orchestrate is derived by the verify runner:
//   contract.saga.orderfulfillment.v1.orchestrate
//   → fullPath = "saga.orderfulfillment.v1.orchestrate"
//   → CamelCase each dot-segment → "SagaOrderfulfillmentV1Orchestrate"
//   → prefix "Test" → TestSagaOrderfulfillmentV1Orchestrate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
)

// TestSagaOrderfulfillmentV1Orchestrate asserts the registered saga definition
// matches the contract yaml: step names, compensable config, timeout, retries.
// F5: saga contract test.
func TestSagaOrderfulfillmentV1Orchestrate(t *testing.T) {
	t.Parallel()

	// 数值来源 contracts/saga/orderfulfillment/v1/contract.yaml（saga.timeout/retries + steps.{timeout,retries}）；改 contract.yaml 须同步此处。

	orders := mem.NewOrderRepository()
	inv := mem.NewInventoryStore(map[string]int{"widget": 100})
	pay := mem.NewPaymentStore()
	ship := mem.NewShipmentStore()

	impl, err := sagaimpl.NewImpl(orders, inv, pay, ship)
	require.NoError(t, err, "NewImpl")

	reg, err := of.Register(impl)
	require.NoError(t, err, "of.Register")

	defn, ok := reg.Lookup(of.DefinitionID)
	require.Truef(t, ok, "Lookup(%q): not found in registry", of.DefinitionID)

	// --- definition-level assertions ---

	assert.Equal(t, of.DefinitionID, defn.ID, "Definition.ID")
	assert.Equal(t, testtime.D30s, defn.Timeout, "Definition.Timeout")
	assert.Equal(t, 3, defn.RetryPolicy.MaxAttempts, "Definition.RetryPolicy.MaxAttempts")
	assert.Equal(t, testtime.D100ms, defn.RetryPolicy.BaseInterval, "Definition.RetryPolicy.BaseInterval")

	// --- step assertions ---

	type stepSpec struct {
		name            string
		compensable     bool          // true → Compensate func must be non-nil
		timeout         time.Duration // 0 = no step-level timeout (inherits saga default)
		stepMaxAttempts int           // 0 = no step-level retry override (inherits saga default)
	}

	// stepMaxAttempts: 0 表示该步骤无步骤级 retries override（Step.RetryPolicy.MaxAttempts 零值，不继承 saga 级 maxAttempts:3）
	wantSteps := []stepSpec{
		{name: "reserveInventory", compensable: true, timeout: testtime.D5s, stepMaxAttempts: 0},
		{name: "chargePayment", compensable: true, timeout: 0, stepMaxAttempts: 2},
		{name: "ship", compensable: true, timeout: 0, stepMaxAttempts: 0},
		{name: "notifyUser", compensable: false, timeout: 0, stepMaxAttempts: 0},
	}

	require.Len(t, defn.Steps, len(wantSteps), "len(Steps)")

	for i, want := range wantSteps {
		step := defn.Steps[i]
		assert.Equalf(t, want.name, string(step.Name), "Steps[%d].Name", i)
		if want.compensable {
			assert.NotNilf(t, step.Compensate, "Steps[%d] (%s) Compensate should be non-nil", i, want.name)
		} else {
			assert.Nilf(t, step.Compensate, "Steps[%d] (%s) Compensate should be nil (not compensable per contract)", i, want.name)
		}
		assert.Equalf(t, want.timeout, step.Timeout, "Steps[%d] (%s) Timeout", i, want.name)
		assert.Equalf(t, want.stepMaxAttempts, step.RetryPolicy.MaxAttempts, "Steps[%d] (%s) RetryPolicy.MaxAttempts", i, want.name)
	}
}
