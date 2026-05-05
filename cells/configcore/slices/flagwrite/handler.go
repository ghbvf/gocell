package flagwrite

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	create "github.com/ghbvf/gocell/generated/contracts/http/config/flags/create/v1"
	flagsdelete "github.com/ghbvf/gocell/generated/contracts/http/config/flags/delete/v1"
	toggle "github.com/ghbvf/gocell/generated/contracts/http/config/flags/toggle/v1"
	update "github.com/ghbvf/gocell/generated/contracts/http/config/flags/update/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// FlagWriteResponse is the public DTO for a feature flag write response, retained
// for unit tests that verify the conversion function directly (toFlagWriteResponse).
type FlagWriteResponse struct {
	ID                string    `json:"id"`
	Key               string    `json:"key"`
	Enabled           bool      `json:"enabled"`
	RolloutPercentage int       `json:"rolloutPercentage"`
	Description       string    `json:"description"`
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func toFlagWriteResponse(f *domain.FeatureFlag) FlagWriteResponse {
	return FlagWriteResponse{
		ID:                f.ID,
		Key:               f.Key,
		Enabled:           f.Enabled,
		RolloutPercentage: f.RolloutPercentage,
		Description:       f.Description,
		Version:           f.Version,
		CreatedAt:         f.CreatedAt,
		UpdatedAt:         f.UpdatedAt,
	}
}

// CreateAdapter wraps Service to implement create.Service for http.config.flags.create.v1.
type CreateAdapter struct{ S *Service }

// Create implements create.Service. Key/Enabled/RolloutPercentage/Description decoded by handler_gen.
func (a CreateAdapter) Create(ctx context.Context, req *create.Request) (*create.Response, error) {
	flag, err := a.S.Create(ctx, CreateInput{
		Key:               req.Key,
		Enabled:           req.Enabled,
		RolloutPercentage: int(req.RolloutPercentage),
		Description:       req.Description,
	})
	if err != nil {
		return nil, err
	}
	return &create.Response{Data: toCreateResponseData(flag)}, nil
}

// UpdateAdapter wraps Service to implement update.Service for http.config.flags.update.v1.
type UpdateAdapter struct{ S *Service }

// Update implements update.Service. Key from path param; all body fields decoded and
// range-validated (rolloutPercentage 0-100) by handler_gen before reaching here.
func (a UpdateAdapter) Update(ctx context.Context, req *update.Request) (*update.Response, error) {
	flag, err := a.S.Update(ctx, UpdateInput{
		Key:               req.Key,
		Enabled:           req.Enabled,
		RolloutPercentage: int(req.RolloutPercentage),
		Description:       req.Description,
	})
	if err != nil {
		return nil, err
	}
	return &update.Response{Data: toUpdateResponseData(flag)}, nil
}

// ToggleAdapter wraps Service to implement toggle.Service for http.config.flags.toggle.v1.
type ToggleAdapter struct{ S *Service }

// Toggle implements toggle.Service. Key from path param; Enabled from body, decoded by handler_gen.
func (a ToggleAdapter) Toggle(ctx context.Context, req *toggle.Request) (*toggle.Response, error) {
	flag, err := a.S.Toggle(ctx, req.Key, req.Enabled)
	if err != nil {
		return nil, err
	}
	return &toggle.Response{Data: toToggleResponseData(flag)}, nil
}

// FlagDeleteAdapter wraps Service to implement flagsdelete.Service for http.config.flags.delete.v1.
type FlagDeleteAdapter struct{ S *Service }

// Delete implements flagsdelete.Service. Key from path param, decoded by handler_gen.
func (a FlagDeleteAdapter) Delete(ctx context.Context, req *flagsdelete.Request) (*flagsdelete.Response, error) {
	if err := a.S.Delete(ctx, req.Key); err != nil {
		return nil, err
	}
	return &flagsdelete.Response{}, nil
}

// Handler is the composite route handler for the flagwrite slice.
type Handler struct {
	createH *create.Handler
	updateH *update.Handler
	toggleH *toggle.Handler
	deleteH *flagsdelete.Handler
}

// NewHandler creates a flagwrite Handler with generated per-contract handlers.
// All endpoints are admin-only.
func NewHandler(svc *Service) *Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	return &Handler{
		createH: create.NewHandler(CreateAdapter{svc}, policy),
		updateH: update.NewHandler(UpdateAdapter{svc}, policy),
		toggleH: toggle.NewHandler(ToggleAdapter{svc}, policy),
		deleteH: flagsdelete.NewHandler(FlagDeleteAdapter{svc}, policy),
	}
}

// RegisterRoutes mounts all four flagwrite contracts on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.createH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.updateH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.toggleH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.deleteH.RegisterRoutes(mux)
}

// toCreateResponseData converts a domain.FeatureFlag to create.ResponseData.
func toCreateResponseData(f *domain.FeatureFlag) *create.ResponseData {
	return &create.ResponseData{
		ID:                f.ID,
		Key:               f.Key,
		Enabled:           f.Enabled,
		RolloutPercentage: int64(f.RolloutPercentage),
		Description:       f.Description,
		Version:           int64(f.Version),
		CreatedAt:         f.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         f.UpdatedAt.Format(time.RFC3339),
	}
}

// toUpdateResponseData converts a domain.FeatureFlag to update.ResponseData.
func toUpdateResponseData(f *domain.FeatureFlag) *update.ResponseData {
	return &update.ResponseData{
		ID:                f.ID,
		Key:               f.Key,
		Enabled:           f.Enabled,
		RolloutPercentage: int64(f.RolloutPercentage),
		Description:       f.Description,
		Version:           int64(f.Version),
		CreatedAt:         f.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         f.UpdatedAt.Format(time.RFC3339),
	}
}

// toToggleResponseData converts a domain.FeatureFlag to toggle.ResponseData.
func toToggleResponseData(f *domain.FeatureFlag) *toggle.ResponseData {
	return &toggle.ResponseData{
		ID:                f.ID,
		Key:               f.Key,
		Enabled:           f.Enabled,
		RolloutPercentage: int64(f.RolloutPercentage),
		Description:       f.Description,
		Version:           int64(f.Version),
		CreatedAt:         f.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         f.UpdatedAt.Format(time.RFC3339),
	}
}
