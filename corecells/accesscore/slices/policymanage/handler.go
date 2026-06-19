package policymanage

import (
	"context"
	"errors"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/projection"
	"github.com/ghbvf/gocell/framework/pkg/query"
	policyCreate "github.com/ghbvf/gocell/generated/contracts/http/policy/create/v1"
	policyDelete "github.com/ghbvf/gocell/generated/contracts/http/policy/delete/v1"
	policyGet "github.com/ghbvf/gocell/generated/contracts/http/policy/get/v1"
	policyList "github.com/ghbvf/gocell/generated/contracts/http/policy/list/v1"
	policyUpdate "github.com/ghbvf/gocell/generated/contracts/http/policy/update/v1"
)

// CreateAdapter wraps Service to implement policyCreate.Service.
type CreateAdapter struct{ s *Service }

// Create implements policyCreate.Service.
func (a CreateAdapter) Create(ctx context.Context, req *policyCreate.Request) (policyCreate.CreateResponseObject, error) {
	rules, err := createRulesFromRequest(req)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapCreateError(ce), nil
		}
		return nil, err
	}

	p, err := a.s.Create(ctx, CreateInput{
		Name:        req.Name,
		Description: req.Description,
		Rules:       rules,
	})
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapCreateError(ce), nil
		}
		return nil, err
	}
	return policyCreate.Create201JSONResponse{Data: policyToCreateResponseData(p)}, nil
}

// mapCreateError maps domain errcodes to the create contract's typed error
// responses. There is intentionally no business conflict (409) mapping: the
// server generates a fresh pol-<uuid> id and policies are identified by id, not
// name (Cedar/XACML model — names are non-unique labels), so policy create has
// no client-reachable business conflict. The 409 this route can still return is
// the idempotency-framework status (ClaimBusy) injected by the listener middleware
// on a replayed idempotency key — it never originates from this handler and is
// compute-only (#1591): not declared in endpoints.http.auth.responses, but derived
// by IdempotencyFrameworkStatuses() and folded into the contract surface. KindInvalid
// → 422 (semantic, e.g. rowScope=all or an unknown enum value); everything else → 400.
func mapCreateError(ce *errcode.Error) policyCreate.CreateResponseObject {
	switch ce.Kind {
	case errcode.KindUnauthenticated:
		return policyCreate.Create401ErrorResponse{Body: *ce}
	case errcode.KindPermissionDenied:
		return policyCreate.Create403ErrorResponse{Body: *ce}
	case errcode.KindInvalid:
		return policyCreate.Create422ErrorResponse{Body: *ce}
	default:
		return policyCreate.Create400ErrorResponse{Body: *ce}
	}
}

// GetAdapter wraps Service to implement policyGet.Service.
type GetAdapter struct{ s *Service }

// Get implements policyGet.Service.
func (a GetAdapter) Get(ctx context.Context, req *policyGet.Request) (policyGet.GetResponseObject, error) {
	p, err := a.s.Get(ctx, req.ID)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapGetError(ce), nil
		}
		return nil, err
	}
	// identity projection — masking obligation source becomes the ABAC Decision
	// later; routes resource data through the column-masking funnel (FR-016).
	data, err := projection.NewProjection(authz.IdentityFieldMask(), policyToGetResponseData(p).ToMap())
	if err != nil {
		return nil, err
	}
	return policyGet.Get200JSONResponse{Data: data}, nil
}

func mapGetError(ce *errcode.Error) policyGet.GetResponseObject {
	switch ce.Kind {
	case errcode.KindUnauthenticated:
		return policyGet.Get401ErrorResponse{Body: *ce}
	case errcode.KindPermissionDenied:
		return policyGet.Get403ErrorResponse{Body: *ce}
	case errcode.KindNotFound:
		return policyGet.Get404ErrorResponse{Body: *ce}
	default:
		return policyGet.Get400ErrorResponse{Body: *ce}
	}
}

// UpdateAdapter wraps Service to implement policyUpdate.Service.
type UpdateAdapter struct{ s *Service }

// Update implements policyUpdate.Service.
func (a UpdateAdapter) Update(ctx context.Context, req *policyUpdate.Request) (policyUpdate.UpdateResponseObject, error) {
	rules, err := updateRulesFromRequest(req)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapUpdateError(ce), nil
		}
		return nil, err
	}

	p, err := a.s.Update(ctx, UpdateInput{
		ID:              req.ID,
		Name:            req.Name,
		Description:     req.Description,
		Rules:           rules,
		ExpectedVersion: int(req.ExpectedVersion),
	})
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapUpdateError(ce), nil
		}
		return nil, err
	}
	return policyUpdate.Update200JSONResponse{Data: policyToUpdateResponseData(p)}, nil
}

func mapUpdateError(ce *errcode.Error) policyUpdate.UpdateResponseObject {
	switch ce.Kind {
	case errcode.KindUnauthenticated:
		return policyUpdate.Update401ErrorResponse{Body: *ce}
	case errcode.KindPermissionDenied:
		return policyUpdate.Update403ErrorResponse{Body: *ce}
	case errcode.KindNotFound:
		return policyUpdate.Update404ErrorResponse{Body: *ce}
	case errcode.KindConflict:
		return policyUpdate.Update409ErrorResponse{Body: *ce}
	case errcode.KindInvalid:
		return policyUpdate.Update422ErrorResponse{Body: *ce}
	default:
		return policyUpdate.Update400ErrorResponse{Body: *ce}
	}
}

// DeleteAdapter wraps Service to implement policyDelete.Service.
type DeleteAdapter struct{ s *Service }

// Delete implements policyDelete.Service.
func (a DeleteAdapter) Delete(ctx context.Context, req *policyDelete.Request) (policyDelete.DeleteResponseObject, error) {
	_, err := a.s.Delete(ctx, req.ID, int(req.ExpectedVersion))
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapDeleteError(ce), nil
		}
		return nil, err
	}
	return policyDelete.Delete204NoContentResponse{}, nil
}

func mapDeleteError(ce *errcode.Error) policyDelete.DeleteResponseObject {
	switch ce.Kind {
	case errcode.KindUnauthenticated:
		return policyDelete.Delete401ErrorResponse{Body: *ce}
	case errcode.KindPermissionDenied:
		return policyDelete.Delete403ErrorResponse{Body: *ce}
	case errcode.KindNotFound:
		return policyDelete.Delete404ErrorResponse{Body: *ce}
	case errcode.KindConflict:
		return policyDelete.Delete409ErrorResponse{Body: *ce}
	default:
		return policyDelete.Delete400ErrorResponse{Body: *ce}
	}
}

// ListAdapter wraps Service to implement policyList.Service.
type ListAdapter struct{ s *Service }

// List implements policyList.Service.
func (a ListAdapter) List(ctx context.Context, req *policyList.Request) (policyList.ListResponseObject, error) {
	pageReq := query.PageParams{Cursor: req.Cursor, Limit: int(req.Limit)}
	result, err := a.s.List(ctx, pageReq)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapListError(ce), nil
		}
		return nil, err
	}

	rows := make([]map[string]any, 0, len(result.Items))
	for _, p := range result.Items {
		rows = append(rows, policyToListResponseDataItem(p).ToMap())
	}
	// identity projection — masking obligation source becomes the ABAC Decision
	// later; routes resource data through the column-masking funnel (FR-016).
	data, err := projection.NewProjectionList(authz.IdentityFieldMask(), rows)
	if err != nil {
		return nil, err
	}
	return policyList.List200JSONResponse{
		Data:       data,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

func mapListError(ce *errcode.Error) policyList.ListResponseObject {
	switch ce.Kind {
	case errcode.KindUnauthenticated:
		return policyList.List401ErrorResponse{Body: *ce}
	case errcode.KindPermissionDenied:
		return policyList.List403ErrorResponse{Body: *ce}
	default:
		return policyList.List400ErrorResponse{Body: *ce}
	}
}

// Handler is the composite route handler for the policymanage slice.
// It holds five generated per-contract handlers and mounts all on a single
// RegisterRoutes call, preserving the one-handler-per-slice field shape
// expected by cell_gen.go while delegating HTTP decode/auth to codegen.
type Handler struct {
	createH *policyCreate.Handler
	getH    *policyGet.Handler
	updateH *policyUpdate.Handler
	deleteH *policyDelete.Handler
	listH   *policyList.Handler
}

// NewHandler creates a policymanage Handler using generated per-contract handlers.
// Authorization is contract-derived (#2355): each contract declares
// endpoints.http.permission (policy:read on GET, policy:write on POST/PUT/DELETE —
// both coarse, no resource), so each generated handler builds its own coarse gate via
// the auth.RequirePermissionForContract funnel (→ RequirePermission); this slice only
// forwards the cell resolver. The built-in PDP baseline grants both permissions to
// admin and super-admin.
func NewHandler(svc *Service, resolver authz.MethodPolicyResolver) *Handler {
	return &Handler{
		createH: policyCreate.NewHandler(CreateAdapter{s: svc}, resolver),
		getH:    policyGet.NewHandler(GetAdapter{s: svc}, resolver),
		updateH: policyUpdate.NewHandler(UpdateAdapter{s: svc}, resolver),
		deleteH: policyDelete.NewHandler(DeleteAdapter{s: svc}, resolver),
		listH:   policyList.NewHandler(ListAdapter{s: svc}, resolver),
	}
}

// RegisterRoutes mounts all policymanage contracts on mux.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	if err := h.createH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.getH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.updateH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.deleteH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.listH.RegisterRoutes(mux)
}
