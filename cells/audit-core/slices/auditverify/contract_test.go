package auditverify

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventAuditIntegrityVerifiedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.audit.integrity-verified.v1")

	c.ValidatePayload(t, []byte(`{"valid":true,"first_invalid_index":0,"entries_checked":100}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-iv-1"}`))
	c.MustRejectPayload(t, []byte(`{"valid":true}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
