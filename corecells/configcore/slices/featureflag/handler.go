package featureflag

import (
	"context"
	"errors"
	"time"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	evaluate "github.com/ghbvf/gocell/generated/contracts/http/config/flags/evaluate/v1"
	flagsget "github.com/ghbvf/gocell/generated/contracts/http/config/flags/get/v1"
	flagslist "github.com/ghbvf/gocell/generated/contracts/http/config/flags/list/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/projection"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

// GetAdapter wraps Service to implement flagsget.Service for http.config.flags.get.v1.
type GetAdapter struct{ S *Service }

// Get implements flagsget.Service. Key comes from path param, already decoded by handler_gen.
func (a GetAdapter) Get(ctx context.Context, req *flagsget.Request) (flagsget.GetResponseObject, error) {
	t, err := tenant.FromContext(ctx)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return flagsget.Get403ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}
	flag, err := a.S.GetByKey(ctx, t, req.Key)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) && ce.Code == errcode.ErrFlagNotFound {
			return flagsget.Get404ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}
	// identity projection (epic #1337 PR-12); masking obligation source becomes
	// the ABAC Decision in PR-10.
	data, err := projection.NewProjection(authz.IdentityFieldMask(), toGetResponseData(flag).ToMap())
	if err != nil {
		return nil, err
	}
	return flagsget.Get200JSONResponse{Data: data}, nil
}

// ListAdapter wraps Service to implement flagslist.Service for http.config.flags.list.v1.
type ListAdapter struct{ S *Service }

// List implements flagslist.Service.
func (a ListAdapter) List(ctx context.Context, req *flagslist.Request) (flagslist.ListResponseObject, error) {
	t, err := tenant.FromContext(ctx)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return flagslist.List403ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := a.S.List(ctx, t, pageReq)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(result.Items))
	for _, f := range result.Items {
		rows = append(rows, toListResponseDataItem(f).ToMap())
	}
	// identity projection (epic #1337 PR-12); masking obligation source becomes
	// the ABAC Decision in PR-10.
	data, err := projection.NewProjectionList(authz.IdentityFieldMask(), rows)
	if err != nil {
		return nil, err
	}
	return flagslist.List200JSONResponse{
		Data:       data,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// EvaluateAdapter wraps Service to implement evaluate.Service for http.config.flags.evaluate.v1.
type EvaluateAdapter struct{ S *Service }

// Evaluate implements evaluate.Service. Key from path, Subject from body (decoded by handler_gen).
func (a EvaluateAdapter) Evaluate(ctx context.Context, req *evaluate.Request) (evaluate.EvaluateResponseObject, error) {
	t, err := tenant.FromContext(ctx)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return evaluate.Evaluate403ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}
	result, err := a.S.Evaluate(ctx, t, req.Key, req.Subject)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) && ce.Code == errcode.ErrFlagNotFound {
			return evaluate.Evaluate404ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}
	return evaluate.Evaluate200JSONResponse{Data: &evaluate.ResponseData{
		Key:     result.Key,
		Enabled: result.Enabled,
	}}, nil
}

// Handler is the composite route handler for the featureflag slice.
type Handler struct {
	getH      *flagsget.Handler
	listH     *flagslist.Handler
	evaluateH *evaluate.Handler
}

// NewHandler creates a featureflag Handler with generated per-contract handlers.
// All endpoints are admin-only.
func NewHandler(svc *Service) *Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	return &Handler{
		getH:      flagsget.NewHandler(GetAdapter{svc}, policy),
		listH:     flagslist.NewHandler(ListAdapter{svc}, policy),
		evaluateH: evaluate.NewHandler(EvaluateAdapter{svc}, policy),
	}
}

// RegisterRoutes mounts all three featureflag contracts on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.getH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.listH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.evaluateH.RegisterRoutes(mux)
}

// toGetResponseData converts a domain.FeatureFlag to flagsget.ResponseData.
func toGetResponseData(f *domain.FeatureFlag) flagsget.ResponseData {
	return flagsget.ResponseData{
		ID:                f.ID,
		Key:               f.Key,
		Type:              string(f.Type),
		Enabled:           f.Enabled,
		RolloutPercentage: int64(f.RolloutPercentage),
		Description:       f.Description,
		Version:           int64(f.Version),
		CreatedAt:         f.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         f.UpdatedAt.Format(time.RFC3339),
	}
}

// toListResponseDataItem converts a domain.FeatureFlag to flagslist.ResponseDataItem.
func toListResponseDataItem(f *domain.FeatureFlag) flagslist.ResponseDataItem {
	return flagslist.ResponseDataItem{
		ID:                f.ID,
		Key:               f.Key,
		Type:              string(f.Type),
		Enabled:           f.Enabled,
		RolloutPercentage: int64(f.RolloutPercentage),
		Description:       f.Description,
		Version:           int64(f.Version),
		CreatedAt:         f.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         f.UpdatedAt.Format(time.RFC3339),
	}
}
