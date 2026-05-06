package sessionlogout

import (
	"context"

	deletegen "github.com/ghbvf/gocell/generated/contracts/http/auth/session/delete/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// DeleteAdapter implements deletegen.Service for http.auth.session.delete.v1.
// It adapts Logout(ctx, sessionID, callerUserID) to Delete(ctx, *Request).
// The caller's user ID is extracted from the auth principal in context.
//
// Route-level PasswordResetExempt: the generated handler emits
// auth.Route{PasswordResetExempt: true} so a user whose token carries
// password_reset_required=true can still reach this endpoint to revoke their
// own session (standard self-recovery flow).
type DeleteAdapter struct{ S *Service }

// Delete implements deletegen.Service. The generated handler already validates
// the session UUID path param and decodes it into req.ID.
//
// {id} is a session id, NOT a user id, so the route-level policy cannot be
// SelfOr("id", admin). Session ownership is enforced inside the Service by
// comparing the principal subject against the session's user_id. Baseline
// AuthMiddleware still requires a valid JWT; PasswordResetExempt keeps the
// route reachable while the caller still owes a password reset (standard
// user-self-recovery flow).
func (a DeleteAdapter) Delete(ctx context.Context, req *deletegen.Request) (deletegen.DeleteResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		// Auth middleware guarantees subject presence on protected routes.
		// Reaching this branch means the route was misconfigured as public —
		// fail closed rather than leak a revoke op to an unauthenticated caller.
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, "missing subject")
	}
	callerUserID := p.Subject

	if err := a.S.Logout(ctx, req.ID, callerUserID); err != nil {
		return nil, err
	}
	return deletegen.Delete204NoContentResponse{}, nil
}

// Handler is the route handler for the sessionlogout slice.
type Handler struct {
	deleteH *deletegen.Handler
}

// NewHandler creates a sessionlogout Handler using the generated session-delete handler.
// No per-route policy: the PasswordResetExempt flag is baked into the generated
// handler's RegisterRoutes (auth.Route{PasswordResetExempt: true}), and ownership
// enforcement is done inside the service.
func NewHandler(svc *Service) *Handler {
	return &Handler{
		deleteH: deletegen.NewHandler(DeleteAdapter{svc}, nil),
	}
}

// RegisterRoutes mounts the session-delete contract handler on mux.
// The parameter type is cell.RouteHandler (not cell.RouteMux) because the
// generated handler_gen.go declares RegisterRoutes(mux cell.RouteHandler) — the
// minimum interface that both production RouteMux and stdlib *http.ServeMux
// satisfy. Using the narrower type keeps the slice composable with both
// chi-based routers and the bare ServeMux used in tests.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.deleteH.RegisterRoutes(mux)
}
