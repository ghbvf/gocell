package configread

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	configget "github.com/ghbvf/gocell/generated/contracts/http/config/get/v1"
	internalapig "github.com/ghbvf/gocell/generated/contracts/http/config/internalapi/get/v1"
	configlist "github.com/ghbvf/gocell/generated/contracts/http/config/list/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// GetAdapter wraps Service to implement configget.Service for http.config.get.v1.
type GetAdapter struct{ S *Service }

// Get implements configget.Service. Key comes from path param, already decoded by handler_gen.
func (a GetAdapter) Get(ctx context.Context, req *configget.Request) (configget.GetResponseObject, error) {
	entry, err := a.S.GetByKey(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	return configget.Get200JSONResponse{Data: toGetResponseData(entry)}, nil
}

// ListAdapter wraps Service to implement configlist.Service for http.config.list.v1.
type ListAdapter struct{ S *Service }

// List implements configlist.Service. ParsePageParams is called by handler_gen.
func (a ListAdapter) List(ctx context.Context, req *configlist.Request) (configlist.ListResponseObject, error) {
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := a.S.List(ctx, pageReq)
	if err != nil {
		return nil, err
	}
	items := make([]*configlist.ResponseDataItem, 0, len(result.Items))
	for _, e := range result.Items {
		items = append(items, toListResponseDataItem(e))
	}
	return configlist.List200JSONResponse{
		Data:       items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// InternalGetAdapter wraps Service to implement internalapig.Service for
// http.config.internal.get.v1. Same business logic as GetAdapter; mounted on
// the InternalListener where service-token auth is enforced by the listener chain.
type InternalGetAdapter struct{ S *Service }

// Get implements internalapig.Service.
func (a InternalGetAdapter) Get(ctx context.Context, req *internalapig.Request) (internalapig.GetResponseObject, error) {
	entry, err := a.S.GetByKey(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	return internalapig.Get200JSONResponse{Data: toInternalGetResponseData(entry)}, nil
}

// Handler is the composite route handler for the configread slice.
// It holds three generated per-contract handlers and exposes RegisterRoutes
// (primary: get + list) and RegisterInternalRoutes (internal: get).
type Handler struct {
	getH         *configget.Handler
	listH        *configlist.Handler
	internalGetH *internalapig.Handler
}

// NewHandler creates a configread Handler with generated per-contract handlers.
// Primary endpoints are admin-only; the internal endpoint explicitly passes
// auth.RequireCallerCell("accesscore") as defense-in-depth, complementing the
// auto-injected caller-cell guard from the generated contractSpec's Clients field.
func NewHandler(svc *Service) *Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	internalPolicy := auth.RequireCallerCell("accesscore")
	return &Handler{
		getH:         configget.NewHandler(GetAdapter{svc}, policy),
		listH:        configlist.NewHandler(ListAdapter{svc}, policy),
		internalGetH: internalapig.NewHandler(InternalGetAdapter{svc}, internalPolicy),
	}
}

// RegisterRoutes mounts the primary config-read routes (get + list) on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.getH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.listH.RegisterRoutes(mux)
}

// RegisterInternalRoutes mounts the internal control-plane GET on mux.
// The handler is constructed with an explicit RequireCallerCell("accesscore")
// policy; auth.Mount also auto-injects the same guard from the contractSpec's
// Clients field, providing defense-in-depth.
func (h *Handler) RegisterInternalRoutes(mux kcell.RouteHandler) error {
	return h.internalGetH.RegisterRoutes(mux)
}

// toGetResponseData converts a domain.ConfigEntry to configget.ResponseData.
func toGetResponseData(e *domain.ConfigEntry) *configget.ResponseData {
	value := e.Value
	if e.Sensitive {
		value = dto.RedactedValue
	}
	return &configget.ResponseData{
		ID:        e.ID,
		Key:       e.Key,
		Value:     value,
		Sensitive: e.Sensitive,
		Version:   int64(e.Version),
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
		UpdatedAt: e.UpdatedAt.Format(time.RFC3339),
	}
}

// toListResponseDataItem converts a domain.ConfigEntry to configlist.ResponseDataItem.
func toListResponseDataItem(e *domain.ConfigEntry) *configlist.ResponseDataItem {
	value := e.Value
	if e.Sensitive {
		value = dto.RedactedValue
	}
	return &configlist.ResponseDataItem{
		ID:        e.ID,
		Key:       e.Key,
		Value:     value,
		Sensitive: e.Sensitive,
		Version:   int64(e.Version),
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
		UpdatedAt: e.UpdatedAt.Format(time.RFC3339),
	}
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
