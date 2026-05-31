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

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
)

// TestSagaOrderfulfillmentV1Orchestrate asserts the registered saga definition
// matches the contract yaml: step names, compensable config, timeout, retries.
// F5: saga contract test.
func TestSagaOrderfulfillmentV1Orchestrate(t *testing.T) {
	t.Parallel()

	orders := mem.NewOrderRepository()
	inv := mem.NewInventoryStore(map[string]int{"widget": 100})
	pay := mem.NewPaymentStore()
	ship := mem.NewShipmentStore()

	impl, err := sagaimpl.NewImpl(orders, inv, pay, ship)
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}

	reg, err := of.Register(impl)
	if err != nil {
		t.Fatalf("of.Register: %v", err)
	}

	defn, ok := reg.Lookup(of.DefinitionID)
	if !ok {
		t.Fatalf("Lookup(%q): not found in registry", of.DefinitionID)
	}

	// --- definition-level assertions ---

	if defn.ID != of.DefinitionID {
		t.Errorf("Definition.ID = %q, want %q", defn.ID, of.DefinitionID)
	}

	wantTimeout := 30 * time.Second
	if defn.Timeout != wantTimeout {
		t.Errorf("Definition.Timeout = %v, want %v", defn.Timeout, wantTimeout)
	}

	wantRetryMaxAttempts := 3
	if defn.RetryPolicy.MaxAttempts != wantRetryMaxAttempts {
		t.Errorf("Definition.RetryPolicy.MaxAttempts = %d, want %d",
			defn.RetryPolicy.MaxAttempts, wantRetryMaxAttempts)
	}

	wantBaseInterval := 100 * time.Millisecond
	if defn.RetryPolicy.BaseInterval != wantBaseInterval {
		t.Errorf("Definition.RetryPolicy.BaseInterval = %v, want %v",
			defn.RetryPolicy.BaseInterval, wantBaseInterval)
	}

	// --- step assertions ---

	type stepSpec struct {
		name        string
		compensable bool          // true → Compensate func must be non-nil
		timeout     time.Duration // 0 = no step-level timeout (inherits saga default)
	}

	wantSteps := []stepSpec{
		{name: "reserveInventory", compensable: true, timeout: 5 * time.Second},
		{name: "chargePayment", compensable: true, timeout: 0},
		{name: "ship", compensable: true, timeout: 0},
		{name: "notifyUser", compensable: false, timeout: 0},
	}

	if len(defn.Steps) != len(wantSteps) {
		t.Fatalf("len(Steps) = %d, want %d", len(defn.Steps), len(wantSteps))
	}

	for i, want := range wantSteps {
		step := defn.Steps[i]
		if string(step.Name) != want.name {
			t.Errorf("Steps[%d].Name = %q, want %q", i, step.Name, want.name)
		}
		if want.compensable && step.Compensate == nil {
			t.Errorf("Steps[%d] (%s) Compensate should be non-nil", i, want.name)
		}
		if !want.compensable && step.Compensate != nil {
			t.Errorf("Steps[%d] (%s) Compensate should be nil (not compensable per contract)", i, want.name)
		}
		if step.Timeout != want.timeout {
			t.Errorf("Steps[%d] (%s) Timeout = %v, want %v", i, want.name, step.Timeout, want.timeout)
		}
	}
}
