// Package webhook_signer_violate is a synthetic violation fixture for the
// WEBHOOK-SIGNER-FUNNEL-01 archtest rule. It exercises the one violation form
// that the rule is designed to catch: Header.Set called with a signature-header
// key from outside (Headers).Apply.
//
// DO NOT use this package in production code.
package webhook_signer_violate

import "net/http"

const (
	// Redeclared locally so the fixture module needs no replace directive and no
	// import of kernel/webhook. The rule scans for the STRING VALUE of these
	// constants (EvaluateConstString), not their identity, so a local redeclaration
	// is sufficient to exercise the violation form.
	headerSignature = "webhook-signature"
	headerID        = "webhook-id"
	headerTimestamp = "webhook-timestamp"
)

// forgeSignatureHeader directly writes the webhook-signature header outside
// (Headers).Apply — the canonical violation the rule prohibits.
func forgeSignatureHeader(h http.Header) {
	h.Set("webhook-signature", "v1,forged") // VIOLATION: literal string key
}

// forgeViaConst uses a local const whose value is the signature header name.
func forgeViaConst(h http.Header) {
	h.Set(headerSignature, "v1,forged") // VIOLATION: const that evaluates to "webhook-signature"
}

// forgeIDHeader writes the webhook-id header directly.
func forgeIDHeader(h http.Header) {
	h.Set(headerID, "bad-delivery-id") // VIOLATION: const that evaluates to "webhook-id"
}

// forgeTimestampHeader writes the webhook-timestamp header directly.
func forgeTimestampHeader(h http.Header) {
	h.Set(headerTimestamp, "0") // VIOLATION: const that evaluates to "webhook-timestamp"
}
