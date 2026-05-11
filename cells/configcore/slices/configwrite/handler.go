package configwrite

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	configdelete "github.com/ghbvf/gocell/generated/contracts/http/config/delete/v1"
	update "github.com/ghbvf/gocell/generated/contracts/http/config/update/v1"
	write "github.com/ghbvf/gocell/generated/contracts/http/config/write/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// WriteAdapter wraps Service to implement write.Service for http.config.write.v1.
type WriteAdapter struct{ S *Service }

// Write implements write.Service. It maps the generated request to CreateInput
// and converts the domain result to the generated response type.
func (a WriteAdapter) Write(ctx context.Context, req *write.Request) (write.WriteResponseObject, error) {
	var sensitive bool
	if req.Sensitive != nil {
		sensitive = *req.Sensitive
	}
	entry, err := a.S.Create(ctx, CreateInput{
		Key:       req.Key,
		Value:     req.Value,
		Sensitive: sensitive,
	})
	if err != nil {
		return nil, err
	}
	return write.Write201JSONResponse{Data: toWriteResponseData(entry)}, nil
}

// UpdateAdapter wraps Service to implement update.Service for http.config.update.v1.
type UpdateAdapter struct{ S *Service }

// Update implements update.Service. It maps the generated request to UpdateInput
// and converts the domain result to the generated response type.
func (a UpdateAdapter) Update(ctx context.Context, req *update.Request) (update.UpdateResponseObject, error) {
	entry, err := a.S.Update(ctx, UpdateInput{
		Key:             req.Key,
		Value:           req.Value,
		ExpectedVersion: int(req.ExpectedVersion),
	})
	if err != nil {
		return nil, err
	}
	return update.Update200JSONResponse{Data: toUpdateResponseData(entry)}, nil
}

// DeleteAdapter wraps Service to implement configdelete.Service for http.config.delete.v1.
type DeleteAdapter struct{ S *Service }

// Delete implements configdelete.Service.
func (a DeleteAdapter) Delete(ctx context.Context, req *configdelete.Request) (configdelete.DeleteResponseObject, error) {
	if err := a.S.Delete(ctx, req.Key, int(req.ExpectedVersion)); err != nil {
		return nil, err
	}
	return configdelete.Delete204NoContentResponse{}, nil
}

// toWriteResponseData converts a domain.ConfigEntry to write.ResponseData.
func toWriteResponseData(e *domain.ConfigEntry) *write.ResponseData {
	value := e.Value
	if e.Sensitive {
		value = dto.RedactedValue
	}
	return &write.ResponseData{
		ID:        e.ID,
		Key:       e.Key,
		Value:     value,
		Sensitive: e.Sensitive,
		Version:   int64(e.Version),
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
		UpdatedAt: e.UpdatedAt.Format(time.RFC3339),
	}
}

// Handler is the composite route handler for the configwrite slice.
// It holds three generated per-contract handlers and mounts all three on a
// single RegisterRoutes call — preserving the one-handler-per-slice field
// shape expected by cell_gen.go while delegating HTTP decode/auth to codegen.
type Handler struct {
	writeH  *write.Handler
	updateH *update.Handler
	deleteH *configdelete.Handler
}

// NewHandler creates a configwrite Handler using generated per-contract handlers.
// All endpoints are admin-only (auth.AnyRole(auth.RoleAdmin)).
func NewHandler(svc *Service) *Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	return &Handler{
		writeH:  write.NewHandler(WriteAdapter{svc}, policy),
		updateH: update.NewHandler(UpdateAdapter{svc}, policy),
		deleteH: configdelete.NewHandler(DeleteAdapter{svc}, policy),
	}
}

// RegisterRoutes mounts all three configwrite contracts on mux.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	if err := h.writeH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.updateH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.deleteH.RegisterRoutes(mux)
}

// toUpdateResponseData converts a domain.ConfigEntry to update.ResponseData.
func toUpdateResponseData(e *domain.ConfigEntry) *update.ResponseData {
	value := e.Value
	if e.Sensitive {
		value = dto.RedactedValue
	}
	return &update.ResponseData{
		ID:        e.ID,
		Key:       e.Key,
		Value:     value,
		Sensitive: e.Sensitive,
		Version:   int64(e.Version),
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
		UpdatedAt: e.UpdatedAt.Format(time.RFC3339),
	}
}
