package featureflag

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	evaluate "github.com/ghbvf/gocell/generated/contracts/http/config/flags/evaluate/v1"
	flagsget "github.com/ghbvf/gocell/generated/contracts/http/config/flags/get/v1"
	flagslist "github.com/ghbvf/gocell/generated/contracts/http/config/flags/list/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// FeatureFlagResponse is the public DTO for FeatureFlag, retained for unit
// tests that verify the conversion function directly (TestToFeatureFlagResponse_NilInput,
// TestFeatureFlagResponse_Fields).
type FeatureFlagResponse struct {
	ID                string    `json:"id"`
	Key               string    `json:"key"`
	Type              string    `json:"type"`
	Enabled           bool      `json:"enabled"`
	RolloutPercentage int       `json:"rolloutPercentage"`
	Description       string    `json:"description"`
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func toFeatureFlagResponse(f *domain.FeatureFlag) FeatureFlagResponse {
	if f == nil {
		return FeatureFlagResponse{}
	}
	return FeatureFlagResponse{
		ID: f.ID, Key: f.Key, Type: string(f.Type),
		Enabled: f.Enabled, RolloutPercentage: f.RolloutPercentage,
		Description: f.Description, Version: f.Version,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

// EvaluateResultResponse is the public DTO for EvaluateResult, retained for
// unit tests.
type EvaluateResultResponse struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

func toEvaluateResultResponse(r *EvaluateResult) EvaluateResultResponse {
	if r == nil {
		return EvaluateResultResponse{}
	}
	return EvaluateResultResponse{Key: r.Key, Enabled: r.Enabled}
}

// GetAdapter wraps Service to implement flagsget.Service for http.config.flags.get.v1.
type GetAdapter struct{ S *Service }

// Get implements flagsget.Service. Key comes from path param, already decoded by handler_gen.
func (a GetAdapter) Get(ctx context.Context, req *flagsget.Request) (*flagsget.Response, error) {
	flag, err := a.S.GetByKey(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	return &flagsget.Response{Data: toGetResponseData(flag)}, nil
}

// ListAdapter wraps Service to implement flagslist.Service for http.config.flags.list.v1.
type ListAdapter struct{ S *Service }

// List implements flagslist.Service.
func (a ListAdapter) List(ctx context.Context, req *flagslist.Request) (*flagslist.Response, error) {
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := a.S.List(ctx, pageReq)
	if err != nil {
		return nil, err
	}
	items := make([]*flagslist.ResponseDataItem, 0, len(result.Items))
	for _, f := range result.Items {
		items = append(items, toListResponseDataItem(f))
	}
	return &flagslist.Response{
		Data:       items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// EvaluateAdapter wraps Service to implement evaluate.Service for http.config.flags.evaluate.v1.
type EvaluateAdapter struct{ S *Service }

// Evaluate implements evaluate.Service. Key from path, Subject from body (decoded by handler_gen).
func (a EvaluateAdapter) Evaluate(ctx context.Context, req *evaluate.Request) (*evaluate.Response, error) {
	result, err := a.S.Evaluate(ctx, req.Key, req.Subject)
	if err != nil {
		return nil, err
	}
	return &evaluate.Response{Data: &evaluate.ResponseData{
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
func toGetResponseData(f *domain.FeatureFlag) *flagsget.ResponseData {
	return &flagsget.ResponseData{
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
func toListResponseDataItem(f *domain.FeatureFlag) *flagslist.ResponseDataItem {
	return &flagslist.ResponseDataItem{
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
