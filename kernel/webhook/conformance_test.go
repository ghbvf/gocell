package webhook_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/kernel/webhook/webhooktest"
)

// TestSignerVerifierConformance wires the HMAC implementations into the
// sign↔verify behavioral contract suite defined in
// kernel/webhook/webhooktest. Any future Signer/Verifier implementation
// must produce a sibling call to RunSignerVerifierConformance in its own
// package.
func TestSignerVerifierConformance(t *testing.T) {
	webhooktest.RunSignerVerifierConformance(t,
		// newSigner: direct match to webhook.NewHMACSigner signature.
		webhook.NewHMACSigner,
		// newVerifier: adapt NewHMACVerifier using a clockmock pinned to ts.
		// clockmock is imported here (test file) so webhooktest/conformance.go
		// (non-_test.go) stays within kernel-isolation rules.
		func(ts time.Time) (webhook.Verifier, error) {
			return webhook.NewHMACVerifier(clockmock.New(ts))
		},
	)
}
