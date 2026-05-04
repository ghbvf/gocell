package sessionlogin

import (
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestHttpAuthLoginV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.login.v1")

	c.ValidateRequest(t, []byte(`{"username":"alice","password":"secret12"}`))
	c.ValidateResponse(t, []byte(`{"data":{"accessToken":"tok","refreshToken":"rtok",`+
		`"expiresAt":"2026-01-01T00:00:00Z","sessionId":"sess-1","userId":"usr-1",`+
		`"passwordResetRequired":false}}`))
	c.MustRejectRequest(t, []byte(`{"username":"alice"}`))
	// Per ADR-202605031600 v1 schema evolution, response schema is lenient:
	// unknown fields are accepted so v1 can grow optional fields without
	// breaking clients. Lock down the lenient behavior here.
	c.ValidateResponse(t, []byte(`{"data":{"accessToken":"x","refreshToken":"y",`+
		`"expiresAt":"2026-01-01T00:00:00Z","sessionId":"s","userId":"u",`+
		`"passwordResetRequired":false,"unexpected":"x"}}`))

	// Negative cases: required fields missing from response must be rejected.
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{
			"missing sessionId",
			[]byte(`{"data":{"accessToken":"x","refreshToken":"y",` +
				`"expiresAt":"2026-01-01T00:00:00Z","userId":"u","passwordResetRequired":false}}`),
		},
		{
			"missing userId",
			[]byte(`{"data":{"accessToken":"x","refreshToken":"y",` +
				`"expiresAt":"2026-01-01T00:00:00Z","sessionId":"s","passwordResetRequired":false}}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.MustRejectResponse(t, tc.body)
		})
	}
}

func TestEventSessionCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.session.created.v1")

	c.ValidatePayload(t, []byte(`{"sessionId":"sess-1","userId":"usr-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-123"}`))
	c.MustRejectPayload(t, []byte(`{"sessionId":"sess-1"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
