package webhook_test

// spec_test.go contains table-driven unit tests for the ReceiverSpec and
// DispatchSpec validation methods. These types are added by the impl agent in
// spec.go — the tests form the RED baseline.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/webhook"
)

// TestReceiverSpecValidate_ValidAllFieldsSet verifies that ReceiverSpec.Validate()
// returns nil when ContractID, SourceID, and CellID are all non-empty.
func TestReceiverSpecValidate_ValidAllFieldsSet(t *testing.T) {
	spec := webhook.ReceiverSpec{
		ContractID: "webhook.stripe.events.v1",
		SourceID:   "stripe",
		CellID:     "paymentcore",
	}
	require.NoError(t, spec.Validate())
}

// TestDispatchSpecValidate_ValidAllFieldsSet verifies that DispatchSpec.Validate()
// returns nil when all three fields are non-empty.
func TestDispatchSpecValidate_ValidAllFieldsSet(t *testing.T) {
	spec := webhook.DispatchSpec{
		ContractID: "webhook.shopify.dispatch.v1",
		SourceID:   "shopify",
		CellID:     "ordercore",
	}
	require.NoError(t, spec.Validate())
}

// TestReceiverSpecValidate_MissingFields verifies that ReceiverSpec.Validate()
// returns an error when any of the three required fields is empty.
func TestReceiverSpecValidate_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		spec webhook.ReceiverSpec
	}{
		{
			name: "missing ContractID",
			spec: webhook.ReceiverSpec{
				ContractID: "",
				SourceID:   "stripe",
				CellID:     "paymentcore",
			},
		},
		{
			name: "missing SourceID",
			spec: webhook.ReceiverSpec{
				ContractID: "webhook.stripe.events.v1",
				SourceID:   "",
				CellID:     "paymentcore",
			},
		},
		{
			name: "missing CellID",
			spec: webhook.ReceiverSpec{
				ContractID: "webhook.stripe.events.v1",
				SourceID:   "stripe",
				CellID:     "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			assert.Error(t, err, "ReceiverSpec.Validate() should return error when a required field is empty")
		})
	}
}

// TestDispatchSpecValidate_MissingFields verifies that DispatchSpec.Validate()
// returns an error when any of the three required fields is empty.
func TestDispatchSpecValidate_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		spec webhook.DispatchSpec
	}{
		{
			name: "missing ContractID",
			spec: webhook.DispatchSpec{
				ContractID: "",
				SourceID:   "shopify",
				CellID:     "ordercore",
			},
		},
		{
			name: "missing SourceID",
			spec: webhook.DispatchSpec{
				ContractID: "webhook.shopify.dispatch.v1",
				SourceID:   "",
				CellID:     "ordercore",
			},
		},
		{
			name: "missing CellID",
			spec: webhook.DispatchSpec{
				ContractID: "webhook.shopify.dispatch.v1",
				SourceID:   "shopify",
				CellID:     "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			assert.Error(t, err, "DispatchSpec.Validate() should return error when a required field is empty")
		})
	}
}

// TestReceiverSpec_ZeroValueIsInvalid asserts that the zero value of ReceiverSpec
// fails Validate() — all three fields are required.
func TestReceiverSpec_ZeroValueIsInvalid(t *testing.T) {
	var spec webhook.ReceiverSpec
	assert.Error(t, spec.Validate(), "zero-value ReceiverSpec must fail Validate()")
}

// TestDispatchSpec_ZeroValueIsInvalid asserts that the zero value of DispatchSpec
// fails Validate().
func TestDispatchSpec_ZeroValueIsInvalid(t *testing.T) {
	var spec webhook.DispatchSpec
	assert.Error(t, spec.Validate(), "zero-value DispatchSpec must fail Validate()")
}
