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
	c.ValidatePayload(t, []byte(`{"reason":"missing_header","clientIp":"1.2.3.4"}`))
	c.ValidatePayload(t, []byte(`{"reason":"wrong_credentials","clientIp":"10.0.0.1"}`))
	c.ValidatePayload(t, []byte(`{"reason":"rate_limited"}`))
	// clientIp is optional
	c.ValidatePayload(t, []byte(`{"reason":"missing_header"}`))

	// --- payload: invalid reason (enum constraint) ---
	c.MustRejectPayload(t, []byte(`{"reason":"unknown_reason"}`))
	c.MustRejectPayload(t, []byte(`{"reason":""}`))
	// missing required field
	c.MustRejectPayload(t, []byte(`{"clientIp":"1.2.3.4"}`))
	c.MustRejectPayload(t, []byte(`{}`))

	// --- headers ---
	c.ValidateHeaders(t, []byte(`{"eventId":"evt-bootstrap-1"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
