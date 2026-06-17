package configpublish

import (
	"context"
	"errors"
	"time"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/corecells/configcore/internal/dto"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	configpublishgen "github.com/ghbvf/gocell/generated/contracts/http/config/publish/v1"
	rollbackgen "github.com/ghbvf/gocell/generated/contracts/http/config/rollback/v1"
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
		var ce *errcode.Error
		if errors.As(err, &ce) {
			switch ce.Code {
			case errcode.ErrAuthForbidden:
				return configpublishgen.Publish403ErrorResponse{Body: *ce}, nil
			case errcode.ErrConfigRepoNotFound:
				return configpublishgen.Publish404ErrorResponse{Body: *ce}, nil
			}
		}
		return nil, err
	}
	return configpublishgen.Publish201JSONResponse{Data: toPublishResponseData(version)}, nil
}

// RollbackAdapter wraps Service to implement rollbackgen.Service for http.config.rollback.v1.
type RollbackAdapter struct{ S *Service }

// Rollback implements rollbackgen.Service. Key comes from path param; Version from body;
// ExpectedVersion is the CAS guard for the current entry.
func (a RollbackAdapter) Rollback(ctx context.Context, req *rollbackgen.Request) (rollbackgen.RollbackResponseObject, error) {
	entry, err := a.S.Rollback(ctx, req.Key, int(req.Version), int(req.ExpectedVersion))
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			switch ce.Code {
			case errcode.ErrAuthForbidden:
				return rollbackgen.Rollback403ErrorResponse{Body: *ce}, nil
			case errcode.ErrConfigRepoNotFound:
				return rollbackgen.Rollback404ErrorResponse{Body: *ce}, nil
			case errcode.ErrVersionConflict:
				return rollbackgen.Rollback409ErrorResponse{Body: *ce}, nil
			}
		}
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
// Both endpoints are gated via the contract-derived resolver
// (endpoints.http.permission overlay, #2205); the ABAC PDP decides — baseline
// grants admin/super-admin (PR-10b #1348).
func NewHandler(svc *Service, resolver authz.MethodPolicyResolver) *Handler {
	return &Handler{
		publishH:  configpublishgen.NewHandler(PublishAdapter{svc}, resolver),
		rollbackH: rollbackgen.NewHandler(RollbackAdapter{svc}, resolver),
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
		ConfigID:  v.ConfigID,
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
