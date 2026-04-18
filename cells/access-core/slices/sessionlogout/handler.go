package sessionlogout

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for session logout.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-logout Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleLogout handles DELETE /api/v1/access/sessions/{id}.
//
// Ownership is enforced by the service: only the caller's own session (subject
// from the verified JWT) can be revoked. A request targeting another user's
// session id gets the same 404 as a request for a non-existent session id —
// hiding enumeration of session ids belonging to other users.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	callerUserID, ok := ctxkeys.SubjectFrom(r.Context())
	if !ok || callerUserID == "" {
		// Auth middleware guarantees subject presence on protected routes.
		// Reaching this branch means the route was misconfigured as public —
		// fail closed rather than leak a revoke op to an unauthenticated caller.
		httputil.WriteDomainError(r.Context(), w,
			errcode.New(errcode.ErrAuthInvalidToken, "missing subject"))
		return
	}

	if err := h.svc.Logout(r.Context(), sessionID, callerUserID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
