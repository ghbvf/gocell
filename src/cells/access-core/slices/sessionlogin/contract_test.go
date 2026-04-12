package sessionlogin

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpAuthLoginV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.login.v1")

	c.ValidateRequest(t, []byte(`{"username":"alice","password":"secret"}`))
	c.ValidateResponse(t, []byte(`{"data":{"accessToken":"tok","refreshToken":"rtok","expiresAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectRequest(t, []byte(`{"username":"alice"}`))
}

func TestEventSessionCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.created.v1")

	c.ValidatePayload(t, []byte(`{"session_id":"sess-1","user_id":"usr-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-123"}`))
	c.MustRejectPayload(t, []byte(`{"session_id":"sess-1"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
