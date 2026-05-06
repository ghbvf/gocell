package configpublish

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	configpublishgen "github.com/ghbvf/gocell/generated/contracts/http/config/publish/v1"
	rollbackgen "github.com/ghbvf/gocell/generated/contracts/http/config/rollback/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// ConfigVersionResponse is the public DTO for ConfigVersion, retained for
// unit tests that verify the conversion function directly.
type ConfigVersionResponse struct {
	ID          string     `json:"id"`
	ConfigID    string     `json:"configId"`
	Version     int        `json:"version"`
	Value       string     `json:"value"`
	Sensitive   bool       `json:"sensitive"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

func toConfigVersionResponse(v *domain.ConfigVersion) ConfigVersionResponse {
	if v == nil {
		return ConfigVersionResponse{}
	}
	value := v.Value
	if v.Sensitive {
		value = dto.RedactedValue
	}
	return ConfigVersionResponse{
		ID: v.ID, ConfigID: v.ConfigID, Version: v.Version,
		Value: value, Sensitive: v.Sensitive, PublishedAt: v.PublishedAt,
	}
}

// PublishAdapter wraps Service to implement configpublishgen.Service for http.config.publish.v1.
type PublishAdapter struct{ S *Service }

// Publish implements configpublishgen.Service. Key comes from path param, already decoded by handler_gen.
func (a PublishAdapter) Publish(ctx context.Context, req *configpublishgen.Request) (configpublishgen.PublishResponseObject, error) {
	version, err := a.S.Publish(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	return configpublishgen.Publish201JSONResponse{Data: toPublishResponseData(version)}, nil
}

// RollbackAdapter wraps Service to implement rollbackgen.Service for http.config.rollback.v1.
type RollbackAdapter struct{ S *Service }

// Rollback implements rollbackgen.Service. Key comes from path param; Version from body.
func (a RollbackAdapter) Rollback(ctx context.Context, req *rollbackgen.Request) (rollbackgen.RollbackResponseObject, error) {
	entry, err := a.S.Rollback(ctx, req.Key, int(req.Version))
	if err != nil {
		return nil, err
	}
	return rollbackgen.Rollback200JSONResponse{Data: toRollbackResponseData(entry)}, nil
}

// Handler is the composite route handler for the configpublish slice.
type Handler struct {
	publishH  *configpublishgen.Handler
	rollbackH *rollbackgen.Handler
}

// NewHandler creates a configpublish Handler with generated per-contract handlers.
// Both endpoints are admin-only.
func NewHandler(svc *Service) *Handler {
	policy := auth.AnyRole(auth.RoleAdmin)
	return &Handler{
		publishH:  configpublishgen.NewHandler(PublishAdapter{svc}, policy),
		rollbackH: rollbackgen.NewHandler(RollbackAdapter{svc}, policy),
	}
}

// RegisterRoutes mounts both configpublish contracts on mux.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	if err := h.publishH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.rollbackH.RegisterRoutes(mux)
}

// toPublishResponseData converts a domain.ConfigVersion to configpublishgen.ResponseData.
func toPublishResponseData(v *domain.ConfigVersion) *configpublishgen.ResponseData {
	if v == nil {
		return &configpublishgen.ResponseData{}
	}
	value := v.Value
	if v.Sensitive {
		value = dto.RedactedValue
	}
	d := &configpublishgen.ResponseData{
		ID:        v.ID,
		ConfigId:  v.ConfigID,
		Version:   int64(v.Version),
		Value:     value,
		Sensitive: v.Sensitive,
	}
	if v.PublishedAt != nil {
		d.PublishedAt = v.PublishedAt.Format(time.RFC3339)
	}
	return d
}

// toRollbackResponseData converts a domain.ConfigEntry to rollbackgen.ResponseData.
func toRollbackResponseData(e *domain.ConfigEntry) *rollbackgen.ResponseData {
	value := e.Value
	if e.Sensitive {
		value = dto.RedactedValue
	}
	return &rollbackgen.ResponseData{
		ID:        e.ID,
		Key:       e.Key,
		Value:     value,
		Sensitive: e.Sensitive,
		Version:   int64(e.Version),
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
		UpdatedAt: e.UpdatedAt.Format(time.RFC3339),
	}
}
