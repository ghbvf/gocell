package sessionrefresh

import (
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestHttpAuthRefreshV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.refresh.v1")

	c.ValidateRequest(t, []byte(`{"refreshToken":"old-token-with-min-len20"}`))
	c.ValidateResponse(t, []byte(`{"data":{"accessToken":"new","refreshToken":"new-r",`+
		`"expiresAt":"2026-01-01T00:00:00Z","sessionId":"sess-1","userId":"usr-1",`+
		`"passwordResetRequired":false}}`))
	c.ValidateErrorResponse(t, http.StatusServiceUnavailable,
		[]byte(`{"error":{"code":"ERR_SERVICE_UNAVAILABLE","message":"service unavailable","details":{}}}`))
	c.MustRejectRequest(t, []byte(`{"refreshToken":"t","extra":"bad"}`))
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
