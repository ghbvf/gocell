package sessionrefresh

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpAuthRefreshV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.refresh.v1")

	c.ValidateRequest(t, []byte(`{"refreshToken":"old-tok"}`))
	c.ValidateResponse(t, []byte(`{"data":{"accessToken":"new","refreshToken":"new-r","expiresAt":"2026-01-01T00:00:00Z","passwordResetRequired":false}}`))
	c.MustRejectRequest(t, []byte(`{"refreshToken":"t","extra":"bad"}`))
	// Schema enforces additionalProperties:false — unknown fields must be rejected.
	c.MustRejectResponse(t, []byte(`{"data":{"accessToken":"x","refreshToken":"y","expiresAt":"2026-01-01T00:00:00Z","passwordResetRequired":false,"unexpected":"x"}}`))
}
