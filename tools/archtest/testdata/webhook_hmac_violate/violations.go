// Package webhook_hmac_violate is a synthetic violation fixture for the
// WEBHOOK-HMAC-FUNNEL-01 archtest rule. Each violation form is exercised once
// so the reverse self-test (TestWebhookHMACFunnel_ReverseFixture) can assert
// the corresponding rule fires.
//
// DO NOT use this package in production code.
package webhook_hmac_violate

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
)

// ── A1 violation: crypto/hmac.New outside kernel/webhook/signer.go ───────────
// scanWebhookHMACNew must detect this call (the file is violations.go, not
// signer.go).
func forgeMAC(secret, payload []byte) []byte {
	mac := hmac.New(sha256.New, secret) // VIOLATION A1
	mac.Write(payload)
	return mac.Sum(nil)
}

// ── A2 violation: signature comparison via bytes.Equal instead of hmac.Equal ─
// scanWebhookBytesEqual must detect this call.
func compareSig(a, b []byte) bool {
	return bytes.Equal(a, b) // VIOLATION A2
}
