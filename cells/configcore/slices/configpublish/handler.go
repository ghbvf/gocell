package configpublish

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract spec literals — cross-checked against
// contracts/http/config/{publish,rollback}/v1/contract.yaml by FMT-18.
var (
	specConfigPublish = wrapper.ContractSpec{
		ID: "http.config.publish.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/config/{key}/publish",
	}
	specConfigRollback = wrapper.ContractSpec{
		ID: "http.config.rollback.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/config/{key}/rollback",
	}
)

// ConfigVersionResponse is the public DTO for ConfigVersion.
// Sensitive snapshots have Value redacted to dto.RedactedValue; the Sensitive
// flag is always surfaced so clients can render appropriately (mirrors
// dto.ToConfigEntryResponse — see H2-2 CONFIGPUBLISH-REDACT-01).
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

// Handler provides HTTP endpoints for config publish operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a config-publish Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers configpublish routes with admin-only policies
// via auth.Mount so every request emits a contract-tagged span.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	if err := auth.Mount(mux, auth.Route{
		Contract: specConfigPublish,
		Handler:  http.HandlerFunc(h.HandlePublish),
		Policy:   auth.AnyRole(auth.RoleAdmin),
	}); err != nil {
		return err
	}
	if err := auth.Mount(mux, auth.Route{
		Contract: specConfigRollback,
		Handler:  http.HandlerFunc(h.HandleRollback),
		Policy:   auth.AnyRole(auth.RoleAdmin),
	}); err != nil {
		return err
	}
	return nil
}

// HandlePublish handles POST /{key}/publish — publishes a config entry.
// Admin-only: publishing changes the active config version, a high-risk
// integrity-affecting operation. Default-deny per K8s/Kratos/go-zero
// convention; authentication alone is not enough.
func (h *Handler) HandlePublish(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	version, err := h.svc.Publish(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toConfigVersionResponse(version)})
}

// HandleRollback handles POST /{key}/rollback — rolls back a config entry.
// Admin-only: rollback re-activates a prior snapshot and is at least as
// privileged as publish. See HandlePublish for the rationale.
func (h *Handler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Version int `json:"version"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	entry, err := h.svc.Rollback(r.Context(), key, req.Version)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": dto.ToConfigEntryResponse(entry)})
}
