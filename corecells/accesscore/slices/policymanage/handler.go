package policymanage

import (
	"context"
	"errors"

	policyCreate "github.com/ghbvf/gocell/generated/contracts/http/policy/create/v1"
	policyDelete "github.com/ghbvf/gocell/generated/contracts/http/policy/delete/v1"
	policyGet "github.com/ghbvf/gocell/generated/contracts/http/policy/get/v1"
	policyList "github.com/ghbvf/gocell/generated/contracts/http/policy/list/v1"
	policyUpdate "github.com/ghbvf/gocell/generated/contracts/http/policy/update/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// CreateAdapter wraps Service to implement policyCreate.Service.
type CreateAdapter struct{ S *Service }

// Create implements policyCreate.Service.
func (a CreateAdapter) Create(ctx context.Context, req *policyCreate.Request) (policyCreate.CreateResponseObject, error) {
	rules, err := createRulesFromRequest(req)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return policyCreate.Create400ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}

	p, err := a.S.Create(ctx, CreateInput{
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

func mapCreateError(ce *errcode.Error) policyCreate.CreateResponseObject {
	switch ce.Kind {
	case errcode.KindUnauthenticated:
		return policyCreate.Create401ErrorResponse{Body: *ce}
	case errcode.KindPermissionDenied:
		return policyCreate.Create403ErrorResponse{Body: *ce}
	case errcode.KindConflict:
		return policyCreate.Create409ErrorResponse{Body: *ce}
	default:
		return policyCreate.Create400ErrorResponse{Body: *ce}
	}
}

// GetAdapter wraps Service to implement policyGet.Service.
type GetAdapter struct{ S *Service }

// Get implements policyGet.Service.
func (a GetAdapter) Get(ctx context.Context, req *policyGet.Request) (policyGet.GetResponseObject, error) {
	p, err := a.S.Get(ctx, req.ID)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapGetError(ce), nil
		}
		return nil, err
	}
	return policyGet.Get200JSONResponse{Data: policyToGetResponseData(p)}, nil
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
type UpdateAdapter struct{ S *Service }

// Update implements policyUpdate.Service.
func (a UpdateAdapter) Update(ctx context.Context, req *policyUpdate.Request) (policyUpdate.UpdateResponseObject, error) {
	rules, err := updateRulesFromRequest(req)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return policyUpdate.Update400ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}

	p, err := a.S.Update(ctx, UpdateInput{
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
	default:
		return policyUpdate.Update400ErrorResponse{Body: *ce}
	}
}

// DeleteAdapter wraps Service to implement policyDelete.Service.
type DeleteAdapter struct{ S *Service }

// Delete implements policyDelete.Service.
func (a DeleteAdapter) Delete(ctx context.Context, req *policyDelete.Request) (policyDelete.DeleteResponseObject, error) {
	_, err := a.S.Delete(ctx, req.ID, int(req.ExpectedVersion))
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
type ListAdapter struct{ S *Service }

// List implements policyList.Service.
func (a ListAdapter) List(ctx context.Context, req *policyList.Request) (policyList.ListResponseObject, error) {
	result, err := a.S.List(ctx, req.Cursor, int(req.Limit))
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return mapListError(ce), nil
		}
		return nil, err
	}

	items := make([]*policyList.ResponseDataItem, 0, len(result.Items))
	for _, p := range result.Items {
		items = append(items, policyToListResponseDataItem(p))
	}
	return policyList.List200JSONResponse{
		Data:       items,
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
// All endpoints are admin-only (auth.AnyRole(auth.RoleAdmin)).
func NewHandler(svc *Service) *Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	return &Handler{
		createH: policyCreate.NewHandler(CreateAdapter{svc}, policy),
		getH:    policyGet.NewHandler(GetAdapter{svc}, policy),
		updateH: policyUpdate.NewHandler(UpdateAdapter{svc}, policy),
		deleteH: policyDelete.NewHandler(DeleteAdapter{svc}, policy),
		listH:   policyList.NewHandler(ListAdapter{svc}, policy),
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
