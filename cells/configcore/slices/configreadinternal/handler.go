package configreadinternal

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	internalapig "github.com/ghbvf/gocell/generated/contracts/http/config/internalapi/get/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// InternalGetAdapter wraps Service to implement internalapig.Service for
// http.config.internal.get.v1. Same read logic as the public configread
// GetAdapter; mounted on the InternalListener where service-token auth is
// enforced by the listener chain.
type InternalGetAdapter struct{ S *Service }

// Get implements internalapig.Service.
func (a InternalGetAdapter) Get(ctx context.Context, req *internalapig.Request) (internalapig.GetResponseObject, error) {
	entry, err := a.S.GetByKey(ctx, req.Key)
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
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.internalGetH.RegisterRoutes(mux)
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
