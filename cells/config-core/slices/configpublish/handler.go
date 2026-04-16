package configpublish

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// roleAdmin is the role required to publish or rollback a config entry.
// Mirrors access-core/internal/domain.RoleAdmin which cannot be imported
// directly (cell-internal). Both must stay in sync — see CLAUDE.md "Cell 之间
// 只通过 contract 通信".
const roleAdmin = "admin"

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

// HandlePublish handles POST /{key}/publish — publishes a config entry.
// Admin-only: publishing changes the active config version, a high-risk
// integrity-affecting operation. Default-deny per K8s/Kratos/go-zero
// convention; authentication alone is not enough.
func (h *Handler) HandlePublish(w http.ResponseWriter, r *http.Request) {
	if err := auth.RequireAnyRole(r.Context(), roleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	key := r.PathValue("key")

	version, err := h.svc.Publish(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toConfigVersionResponse(version)})
}

// HandleRollback handles POST /{key}/rollback — rolls back a config entry.
// Admin-only: rollback re-activates a prior snapshot and is at least as
// privileged as publish. See HandlePublish for the rationale.
func (h *Handler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	if err := auth.RequireAnyRole(r.Context(), roleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

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
