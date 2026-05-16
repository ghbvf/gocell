package configread

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	configget "github.com/ghbvf/gocell/generated/contracts/http/config/get/v1"
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

// Handler is the composite route handler for the public configread slice.
// It holds the get + list generated handlers and exposes RegisterRoutes
// (primary listener: get + list). The internal control-plane GET is owned
// by the sibling configreadinternal slice.
type Handler struct {
	getH  *configget.Handler
	listH *configlist.Handler
}

// NewHandler creates a public configread Handler with generated per-contract
// handlers. Both endpoints are admin-only (auth.AnyRole(auth.RoleAdmin)).
func NewHandler(svc *Service) *Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	return &Handler{
		getH:  configget.NewHandler(GetAdapter{svc}, policy),
		listH: configlist.NewHandler(ListAdapter{svc}, policy),
	}
}

// RegisterRoutes mounts the public config-read routes (get + list) on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.getH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.listH.RegisterRoutes(mux)
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
