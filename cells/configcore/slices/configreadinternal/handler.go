package configreadinternal

import (
	"context"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	internalapig "github.com/ghbvf/gocell/generated/contracts/http/config/internalapi/get/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/cellmw"
)

// headerTenantID is the HTTP header name for the tenant identifier on
// the internal control-plane path. Must match the contract comment and the
// accesscore configgetter HTTP adapter that sets this header.
const headerTenantID = "X-Tenant-ID"

// internalTenantCtxKey is the unexported context key used to ferry the raw
// X-Tenant-ID header value into the handler. Using a local unexported struct
// avoids writing to ctxkeys.TenantID, which is locked to auth-boundary callers
// by CTXKEYS-PRINCIPAL-WRITE-CALLER-01.
type internalTenantCtxKey struct{}

// internalTenantFromCtx retrieves the raw X-Tenant-ID header value that was
// stashed by injectInternalTenant. Returns "" if the header was absent.
func internalTenantFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(internalTenantCtxKey{}).(string)
	return v
}

// injectInternalTenant is the per-request middleware that reads X-Tenant-ID
// from the HTTP header and stores the raw value in ctx under
// internalTenantCtxKey. Parsing (and rejection of invalid/reserved values)
// is deferred to the adapter's Get method so the error path can return a
// typed 400 response.
func injectInternalTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), internalTenantCtxKey{}, r.Header.Get(headerTenantID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// InternalGetAdapter wraps Service to implement internalapig.Service for
// http.config.internal.get.v1. Same read logic as the public configread
// GetAdapter; mounted on the InternalListener where service-token auth is
// enforced by the listener chain.
type InternalGetAdapter struct{ S *Service }

// Get implements internalapig.Service. Derives the tenant from the X-Tenant-ID
// header (stashed by injectInternalTenant); rejects missing, malformed, or
// reserved nil-UUID values with a typed 400 response (fail-closed).
func (a InternalGetAdapter) Get(ctx context.Context, req *internalapig.Request) (internalapig.GetResponseObject, error) {
	raw := internalTenantFromCtx(ctx)
	t, err := tenant.ParseTenantID(raw)
	if err != nil {
		resp400 := internalapig.Get400ErrorResponse{Body: *errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"invalid or missing X-Tenant-ID header",
		)}
		return resp400, nil //nolint:nilerr // typed-response-envelope: declared 400 returned as typed struct + nil err (cell-patterns.md)
	}
	entry, err := a.S.GetByKey(ctx, t, req.Key)
	if err != nil {
		return nil, err
	}
	return internalapig.Get200JSONResponse{Data: toInternalGetResponseData(entry)}, nil
}

// Handler is the route handler for the internal config-read slice. It holds
// the internal GET generated handler and exposes RegisterRoutes; the cell
// mounts it on the InternalListener (see cells/configcore/cell.go marker).
type Handler struct {
	internalGetH *internalapig.Handler
}

// NewHandler creates an internal configread Handler. The handler is
// constructed with an explicit RequireCallerCell("accesscore") policy.
//
// On layered security: the true defense-in-depth comes from two distinct
// enforcement layers:
//   - Transport/encryption layer: the InternalListener requires a valid
//     service-token (HMAC-SHA256 + nonce replay guard), which is verified
//     before any routing occurs.
//   - Application-layer authorization: RequireCallerCell("accesscore") checks
//     the callerCell claim embedded in the service token.
//
// The explicit RequireCallerCell policy passed here and the guard that
// auth.Mount auto-injects from contractSpec.Clients are the SAME guard applied
// at the same layer — they are not two independent mechanisms. The explicit
// declaration is retained to make the guard visible at the handler construction
// site and to ensure it remains in effect even if contractSpec.Clients drifts
// (preventing a silent guard removal).
func NewHandler(svc *Service) *Handler {
	internalPolicy := auth.RequireCallerCell("accesscore")
	return &Handler{
		internalGetH: internalapig.NewHandler(InternalGetAdapter{svc}, internalPolicy),
	}
}

// RegisterRoutes mounts the internal control-plane GET on mux. The cell wires
// this onto the InternalListener via the +slice:route marker in cell.go.
// The mux is wrapped with injectInternalTenant so the X-Tenant-ID header is
// available to the adapter before any routing occurs.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.internalGetH.RegisterRoutes(cellmw.NewHeaderInjectMux(mux, injectInternalTenant))
}

// toInternalGetResponseData converts a domain.ConfigEntry to internalapig.ResponseData.
func toInternalGetResponseData(e *domain.ConfigEntry) *internalapig.ResponseData {
	value := e.Value
	if e.Sensitive {
		value = dto.RedactedValue
	}
	return &internalapig.ResponseData{
		ID:        e.ID,
		Key:       e.Key,
		Value:     value,
		Sensitive: e.Sensitive,
		Version:   int64(e.Version),
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
		UpdatedAt: e.UpdatedAt.Format(time.RFC3339),
	}
}
