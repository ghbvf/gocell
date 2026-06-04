package auditappendbootstrap_test

import (
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
	c.ValidatePayload(t, []byte(`{"reason":"missing_header","clientIpHash":"a1b2c3d4"}`))
	c.ValidatePayload(t, []byte(`{"reason":"wrong_credentials","clientIpHash":"deadbeef"}`))
	c.ValidatePayload(t, []byte(`{"reason":"rate_limited"}`))
	// clientIpHash is optional
	c.ValidatePayload(t, []byte(`{"reason":"missing_header"}`))

	// --- payload: missing required reason ---
	// Note: reason VALUE validation (the {missing_header, wrong_credentials,
	// rate_limited} whitelist) is runtime-only — contractgen's jsonschema subset
	// does not support the `enum` keyword, so the schema only enforces that
	// reason is a present string. The subscriber Rejects unknown reasons at
	// runtime (auditappendbootstrap.HandleEvent → AppendBootstrapAuthFail).
	c.MustRejectPayload(t, []byte(`{"clientIpHash":"a1b2c3d4"}`))
	c.MustRejectPayload(t, []byte(`{}`))

	// --- headers ---
	c.ValidateHeaders(t, []byte(`{"eventId":"evt-bootstrap-1"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
