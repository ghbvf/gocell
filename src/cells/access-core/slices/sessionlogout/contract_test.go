package sessionlogout

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventSessionRevokedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.revoked.v1")

	// Schema validation: payload requires session_id + user_id, headers requires event_id.
	c.ValidatePayload(t, []byte(`{"session_id":"sess-1","user_id":"usr-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-789"}`))
}
