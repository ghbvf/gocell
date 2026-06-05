package auditappendbootstrap_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// TestEventAuthBootstrapFailedV1Subscribe validates the
// event.auth.bootstrap-failed.v1 payload and headers schemas against the
// consumer's expected wire shape.  This test fulfills the
// contract.event.auth.bootstrap-failed.v1.subscribe entry in
// slice.yaml verify.contract (VERIFY-01 closure).
func TestEventAuthBootstrapFailedV1Subscribe(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.auth.bootstrap-failed.v1")

	// --- payload: valid cases ---
	// clientIpHash must match the schema pattern ^$|^[a-f0-9]{64}$ (#1488 F1):
	// a 64-char lowercase-hex HMAC digest, or absent/empty.
	h64a := strings.Repeat("a1b2c3d4", 8) // 64-hex
	h64b := strings.Repeat("deadbeef", 8) // 64-hex
	upper64 := strings.Repeat("A1B2C3D4", 8)
	c.ValidatePayload(t, []byte(`{"reason":"missing_header","clientIpHash":"`+h64a+`"}`))
	c.ValidatePayload(t, []byte(`{"reason":"wrong_credentials","clientIpHash":"`+h64b+`"}`))
	c.ValidatePayload(t, []byte(`{"reason":"rate_limited"}`))
	// clientIpHash is optional; empty string is also valid (no IP available).
	c.ValidatePayload(t, []byte(`{"reason":"missing_header"}`))
	c.ValidatePayload(t, []byte(`{"reason":"missing_header","clientIpHash":""}`))

	// --- payload: malformed clientIpHash (pattern guard, #1488 F1) ---
	// A short/old hash, plaintext IP, or uppercase hex must be rejected by the
	// schema so a plaintext-laundered value cannot enter the audit ledger.
	c.MustRejectPayload(t, []byte(`{"reason":"missing_header","clientIpHash":"a1b2c3d4"}`))
	c.MustRejectPayload(t, []byte(`{"reason":"missing_header","clientIpHash":"192.0.2.1"}`))
	c.MustRejectPayload(t, []byte(`{"reason":"missing_header","clientIpHash":"`+upper64+`"}`))

	// --- payload: missing required reason ---
	// Note: reason VALUE validation (the {missing_header, wrong_credentials,
	// rate_limited} whitelist) is runtime-only — contractgen's jsonschema subset
	// does not support the `enum` keyword, so the schema only enforces that
	// reason is a present string. The subscriber Rejects unknown reasons at
	// runtime (auditappendbootstrap.HandleEvent → AppendBootstrapAuthFail).
	c.MustRejectPayload(t, []byte(`{"clientIpHash":"`+h64a+`"}`))
	c.MustRejectPayload(t, []byte(`{}`))

	// --- headers ---
	c.ValidateHeaders(t, []byte(`{"eventId":"evt-bootstrap-1"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
