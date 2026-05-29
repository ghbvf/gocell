package contractspec_test

// webhook_spec_test.go extends ContractSpec validation tests for the new
// webhook kind. After the impl agent adds ContractWebhook handling to
// ContractSpec.Validate(), these tests should pass. They are the RED baseline.

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
)

// TestContractSpec_WebhookKind_ValidatesOK verifies that a ContractSpec with
// Kind=ContractWebhook and Transport="http" passes Validate() once the impl
// agent adds the webhook case to the switch. The existing default branch would
// reject it — this test must fail until the impl adds the webhook case.
func TestContractSpec_WebhookKind_ValidatesOK(t *testing.T) {
	t.Parallel()
	spec := contractspec.ContractSpec{
		ID:        "webhook.stripe.events.v1",
		Kind:      cellvocab.ContractWebhook,
		Transport: "http",
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("expected nil for webhook ContractSpec, got: %v", err)
	}
}

// TestContractSpec_WebhookKind_EmptyIDRejected verifies the common validation
// (ID must not be empty) still applies to webhook contracts.
func TestContractSpec_WebhookKind_EmptyIDRejected(t *testing.T) {
	t.Parallel()
	spec := contractspec.ContractSpec{
		ID:        "",
		Kind:      cellvocab.ContractWebhook,
		Transport: "http",
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected error for webhook ContractSpec with empty ID")
	}
	// Assert it is the ID-empty error specifically — not the kind-not-recognized
	// fallback — so the test cannot pass for the wrong reason.
	if !strings.Contains(err.Error(), "ID must not be empty") {
		t.Fatalf("expected an 'ID must not be empty' error, got: %v", err)
	}
}

// TestContractSpec_WebhookKind_EmptyTransportRejected verifies that Transport
// is required for webhook contracts (same as other kinds).
func TestContractSpec_WebhookKind_EmptyTransportRejected(t *testing.T) {
	t.Parallel()
	spec := contractspec.ContractSpec{
		ID:        "webhook.stripe.events.v1",
		Kind:      cellvocab.ContractWebhook,
		Transport: "",
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected error for webhook ContractSpec with empty Transport")
	}
	// Assert it is the Transport-empty error specifically — Kind is a valid
	// webhook here, so the kind-not-recognized branch must not fire.
	if !strings.Contains(err.Error(), "Transport must not be empty") {
		t.Fatalf("expected a 'Transport must not be empty' error, got: %v", err)
	}
	if strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("empty-Transport webhook spec must not hit the kind-not-recognized branch; got: %v", err)
	}
}

// TestContractSpec_WebhookKind_DefaultBranchNoLongerFires verifies that the
// default-branch error message "Kind %q not recognized" is NOT returned for
// ContractWebhook after the impl adds the case.
//
// This is the critical test: before the impl change, Validate() on a webhook
// spec hits the default branch and returns a "not recognized" error. After the
// impl change it should return nil. The test documents exactly which error
// would indicate the impl is still missing.
func TestContractSpec_WebhookKind_DefaultBranchNoLongerFires(t *testing.T) {
	t.Parallel()
	spec := contractspec.ContractSpec{
		ID:        "webhook.stripe.events.v1",
		Kind:      cellvocab.ContractWebhook,
		Transport: "http",
	}
	err := spec.Validate()
	if err != nil {
		// If the default branch still fires, the message contains "not recognized".
		t.Fatalf("ContractWebhook must not hit the default (not recognized) branch; got: %v", err)
	}
}
