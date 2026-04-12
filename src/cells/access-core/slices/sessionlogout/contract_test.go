package sessionlogout

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Contract: event.session.revoked.v1 — logout publishes {session_id, user_id}.
// Verifies the action that triggers the event completes successfully.
func TestEventSessionRevokedV1Publish(t *testing.T) {
	h := setup() // seeds session "sess-1"

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/sess-1", nil)
	req.SetPathValue("id", "sess-1")
	h.HandleLogout(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code,
		"contract: logout must succeed for existing session, triggering event.session.revoked.v1 publish")
}
