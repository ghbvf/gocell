package sessionlogout

import (
	"context"
	"errors"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/httpcookie"
	deletegen "github.com/ghbvf/gocell/generated/contracts/http/auth/session/delete/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/cellmw"
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
		// Reaching this branch means no principal in context, or a principal
		// with no user Subject. The latter happens for service principals
		// (auth.PrincipalService.Subject == "" by design — service tokens
		// identify a callerCell, not a user) and would also occur if this
		// route were misconfigured as public. Either way, sessionlogout is
		// user-owned; fail closed rather than leak a revoke op to a
		// non-user caller.
		return deletegen.Delete401ErrorResponse{Body: *errcode.New(
			errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, "missing subject",
		)}, nil
	}
	callerUserID := p.Subject

	if err := a.S.Logout(ctx, req.ID, callerUserID); err != nil {
		// Map declared business errors (400/404) to generated typed responses
		// per cell-patterns.md §typed response envelope adapter. Undeclared
		// kinds (503 KindUnavailable from infra Wrap, or genuine framework
		// 500) flow through `return nil, err` for httputil.WriteError fallback.
		var ec *errcode.Error
		if errors.As(err, &ec) {
			switch ec.Kind {
			case errcode.KindInvalid:
				return deletegen.Delete400ErrorResponse{Body: *ec}, nil
			case errcode.KindNotFound:
				return deletegen.Delete404ErrorResponse{Body: *ec}, nil
			}
		}
		return nil, err
	}
	httpcookie.ClearRefresh(ctx)
	return deletegen.Delete204NoContentResponse{}, nil
}

// Handler is the route handler for the sessionlogout slice.
type Handler struct {
	deleteH   *deletegen.Handler
	cookieTTL time.Duration
}

// NewHandler creates a sessionlogout Handler using the generated session-delete handler.
// cookieTTL is the refresh-token lifetime; it is forwarded to [httpcookie.Middleware]
// so the wrapper can emit a properly-aged Set-Cookie (for a clear, Middleware
// hard-codes Max-Age=0 regardless of this value).
// No per-route policy: contract.yaml declares auth.serviceOwned:true, so the
// generated handler keeps listener JWT auth and PasswordResetExempt routing while
// ownership enforcement stays inside the service.
func NewHandler(svc *Service, cookieTTL time.Duration) *Handler {
	return &Handler{
		deleteH:   deletegen.NewHandler(DeleteAdapter{svc}),
		cookieTTL: cookieTTL,
	}
}

// RegisterRoutes mounts the session-delete contract handler on mux, wrapped with
// [httpcookie.Middleware] so that a successful 204 response carries
// Set-Cookie: gocell_rt=; ...; Max-Age=0 to delete the refresh cookie.
// The parameter type is cell.RouteHandler (not cell.RouteMux) because the
// generated handler_gen.go declares RegisterRoutes(mux cell.RouteHandler) — the
// minimum interface that both production RouteMux and stdlib *http.ServeMux
// satisfy. Using the narrower type keeps the slice composable with both
// chi-based routers and the bare ServeMux used in tests.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.deleteH.RegisterRoutes(cellmw.NewHeaderInjectMux(mux, httpcookie.Middleware(h.cookieTTL)))
}
