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
	"log/slog"
	"reflect"
	"slices"
)

// ── A1 violation (file-level): crypto/hmac.New outside signer.go ─────────────
// scanWebhookHMACNew must detect this call (the file is violations.go, not
// signer.go). The in-signer.go-but-outside-computeMAC form lives in signer.go.
func forgeMAC(secret, payload []byte) []byte {
	mac := hmac.New(sha256.New, secret) // VIOLATION A1 (outside signer.go)
	mac.Write(payload)
	return mac.Sum(nil)
}

// ── A2 violations: non-constant-time comparison callees ──────────────────────
// scanWebhookBytesEqual must detect each of these.
func compareSig(a, b []byte) bool {
	return bytes.Equal(a, b) || // VIOLATION A2 (bytes.Equal)
		bytes.Compare(a, b) == 0 || // VIOLATION A2 (bytes.Compare)
		slices.Equal(a, b) || // VIOLATION A2 (slices.Equal)
		reflect.DeepEqual(a, b) // VIOLATION A2 (reflect.DeepEqual)
}

// ── B6 violations: slog calls referencing a raw .secret field ────────────────
// scanWebhookSecretSlog must detect BOTH the package-function form and the
// *slog.Logger method form.
type leakyHolder struct{ secret []byte }

var fixtureLogger *slog.Logger

func (h leakyHolder) logIt()       { slog.Info("dump", slog.Any("secret", h.secret)) }          // VIOLATION B6 (pkg func)
func (h leakyHolder) logItMethod() { fixtureLogger.Info("dump", slog.Any("secret", h.secret)) } // VIOLATION B6 (method)
