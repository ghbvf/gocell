package webhook_test

// spec_test.go contains table-driven unit tests for the ReceiverSpec and
// DispatchSpec validation methods. These types are added by the impl agent in
// spec.go — the tests form the RED baseline.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/webhook"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// validReceiverSpecExt returns a fully-populated ReceiverSpec for external
// (webhook_test package) tests. Mirrors the internal validReceiverSpec helper
// in registry_test.go; kept in sync with any ReceiverSpec field additions.
func validReceiverSpecExt() webhook.ReceiverSpec {
	return webhook.ReceiverSpec{
		ContractID:       "webhook.stripe.events.v1",
		SourceID:         "stripe",
		CellID:           "paymentcore",
		PathPattern:      "/api/webhooks/stripe/events",
		DeliveryIDHeader: "X-Delivery-Id",
		TimestampHeader:  "X-Timestamp",
		SignatureHeader:  "X-Signature",
		ToleranceSeconds: 300,
		MaxBodyBytes:     1048576,
	}
}

// TestReceiverSpecValidate_ValidAllFieldsSet verifies that ReceiverSpec.Validate()
// returns nil when all required fields are non-empty and in-range.
func TestReceiverSpecValidate_ValidAllFieldsSet(t *testing.T) {
	spec := validReceiverSpecExt()
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
// returns an error when any required field is empty or out of range.
func TestReceiverSpecValidate_MissingFields(t *testing.T) {
	base := validReceiverSpecExt()
	tests := []struct {
		name  string
		mutFn func(s webhook.ReceiverSpec) webhook.ReceiverSpec
	}{
		{
			name:  "missing ContractID",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.ContractID = ""; return s },
		},
		{
			name:  "missing SourceID",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.SourceID = ""; return s },
		},
		{
			name:  "missing CellID",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.CellID = ""; return s },
		},
		{
			name:  "missing PathPattern",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.PathPattern = ""; return s },
		},
		{
			name:  "missing DeliveryIDHeader",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.DeliveryIDHeader = ""; return s },
		},
		{
			name:  "missing TimestampHeader",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.TimestampHeader = ""; return s },
		},
		{
			name:  "missing SignatureHeader",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.SignatureHeader = ""; return s },
		},
		{
			name:  "zero ToleranceSeconds",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.ToleranceSeconds = 0; return s },
		},
		{
			name:  "negative ToleranceSeconds",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.ToleranceSeconds = -1; return s },
		},
		{
			name:  "zero MaxBodyBytes",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.MaxBodyBytes = 0; return s },
		},
		{
			name:  "negative MaxBodyBytes",
			mutFn: func(s webhook.ReceiverSpec) webhook.ReceiverSpec { s.MaxBodyBytes = -1; return s },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutFn(base).Validate()
			requireWebhookConfigInvalid(t, err)
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
			requireWebhookConfigInvalid(t, err)
		})
	}
}

// TestSpecValidate_InvalidSourceID asserts that a non-empty SourceID that is not
// metric-label-safe (off the [a-z][a-z0-9_-]* shape, or over the length budget)
// fails Validate() for BOTH spec kinds. This is the startup fail-fast that
// prevents a request/worker-time MustValidateLabels panic when the SourceID
// reaches a metric {source} label (regression for the C1/F1 finding): the label
// separators '=' and '|' are the panic triggers; uppercase / leading digit /
// over-length are rejected by the shared SourceID validator.
func TestSpecValidate_InvalidSourceID(t *testing.T) {
	badSources := []struct {
		name   string
		source string
	}{
		{"equals separator", "bad=source"},
		{"pipe separator", "bad|source"},
		{"uppercase", "Stripe"},
		{"leading digit", "1stripe"},
		{"space", "bad source"},
		{"over length budget", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for _, bs := range badSources {
		t.Run("receiver/"+bs.name, func(t *testing.T) {
			s := validReceiverSpecExt()
			s.SourceID = bs.source
			requireWebhookConfigInvalid(t, s.Validate())
		})
		t.Run("dispatch/"+bs.name, func(t *testing.T) {
			s := webhook.DispatchSpec{
				ContractID: "webhook.shopify.dispatch.v1",
				SourceID:   bs.source,
				CellID:     "ordercore",
			}
			requireWebhookConfigInvalid(t, s.Validate())
		})
	}
}

// TestReceiverSpec_ZeroValueIsInvalid asserts that the zero value of ReceiverSpec
// fails Validate() — all three fields are required.
func TestReceiverSpec_ZeroValueIsInvalid(t *testing.T) {
	var spec webhook.ReceiverSpec
	requireWebhookConfigInvalid(t, spec.Validate())
}

// TestDispatchSpec_ZeroValueIsInvalid asserts that the zero value of DispatchSpec
// fails Validate().
func TestDispatchSpec_ZeroValueIsInvalid(t *testing.T) {
	var spec webhook.DispatchSpec
	requireWebhookConfigInvalid(t, spec.Validate())
}

// requireWebhookConfigInvalid asserts err is an *errcode.Error carrying the
// ErrWebhookConfigInvalid / KindInvalid pair that ReceiverSpec.Validate and
// DispatchSpec.Validate always return on a missing field. Local to this
// external (webhook_test) package — the sibling helper in webhook_test.go lives
// in the internal (package webhook) test files and is not visible here.
func requireWebhookConfigInvalid(t *testing.T, err error) {
	t.Helper()
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	require.Equal(t, errcode.ErrWebhookConfigInvalid, ec.Code)
	require.Equal(t, errcode.KindInvalid, ec.Kind)
}
