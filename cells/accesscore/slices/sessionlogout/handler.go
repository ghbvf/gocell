package sessionlogout

import (
	"net/http"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// specSessionDelete declares the contract for the session-delete endpoint,
// cross-checked against contracts/http/auth/session/delete/v1/contract.yaml by FMT-18.
var specSessionDelete = wrapper.ContractSpec{
	ID: "http.auth.session.delete.v1", Kind: "http", Transport: "http",
	Method: "DELETE", Path: "/api/v1/access/sessions/{id}",
}

// Handler provides HTTP endpoints for session logout.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-logout Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers the session-delete route on mux via auth.Mount so
// CH-04/CH-05 governance can correlate this contract to HandleLogout.
//
// {id} is a session id, NOT a user id, so the route-level policy cannot be
// SelfOr("id", admin). Session ownership is enforced inside HandleLogout by
// comparing the principal subject against the session's user_id. Baseline
// AuthMiddleware still requires a valid JWT; PasswordResetExempt keeps the
// route reachable while the caller still owes a password reset (standard
// user-self-recovery flow).
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := auth.Mount(mux, auth.Route{
		Contract:            specSessionDelete,
		Handler:             http.HandlerFunc(h.HandleLogout),
		PasswordResetExempt: true,
	}); err != nil {
		return err
	}
	return nil
}

// HandleLogout handles DELETE /api/v1/access/sessions/{id}.
//
// Ownership is enforced by the service: only the caller's own session (subject
// from the verified JWT) can be revoked. A request targeting another user's
// session id gets the same 404 as a request for a non-existent session id —
// hiding enumeration of session ids belonging to other users.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	p, ok := auth.FromContext(r.Context())
	if !ok || p.Subject == "" {
		// Auth middleware guarantees subject presence on protected routes.
		// Reaching this branch means the route was misconfigured as public —
		// fail closed rather than leak a revoke op to an unauthenticated caller.
		httputil.WriteDomainError(r.Context(), w,
			errcode.New(errcode.ErrAuthInvalidToken, "missing subject"))
		return
	}
	callerUserID := p.Subject

	if err := h.svc.Logout(r.Context(), sessionID, callerUserID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
