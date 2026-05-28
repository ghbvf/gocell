// This fixture file is named signer.go to exercise WEBHOOK-HMAC-FUNNEL-01/A1's
// FUNCTION-level check: crypto/hmac.New here is inside signer.go but NOT inside
// computeMAC, so scanWebhookHMACNew must still flag it (file-level allowlisting
// would wrongly pass this).
//
// DO NOT use this package in production code.
package webhook_hmac_violate

import (
	"crypto/hmac"
	"crypto/sha256"
)

// notComputeMAC sits in signer.go but is not computeMAC — the A1 function-level
// check must fire on this hmac.New.
func notComputeMAC(secret, payload []byte) []byte {
	mac := hmac.New(sha256.New, secret) // VIOLATION A1 (signer.go, non-computeMAC func)
	mac.Write(payload)
	return mac.Sum(nil)
}
