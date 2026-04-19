package flagwrite

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// FlagWriteResponse is the DTO for a feature flag write response.
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

// Handler provides HTTP endpoints for feature flag write operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a flag-write Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleCreate handles POST / — creates a new feature flag.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key               string `json:"key"`
		Enabled           bool   `json:"enabled"`
		RolloutPercentage int    `json:"rolloutPercentage"`
		Description       string `json:"description"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	flag, err := h.svc.Create(r.Context(), CreateInput{
		Key:               req.Key,
		Enabled:           req.Enabled,
		RolloutPercentage: req.RolloutPercentage,
		Description:       req.Description,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toFlagWriteResponse(flag)})
}

// HandleUpdate handles PUT /{key} — updates an existing feature flag.
func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Enabled           bool   `json:"enabled"`
		RolloutPercentage int    `json:"rolloutPercentage"`
		Description       string `json:"description"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	flag, err := h.svc.Update(r.Context(), UpdateInput{
		Key:               key,
		Enabled:           req.Enabled,
		RolloutPercentage: req.RolloutPercentage,
		Description:       req.Description,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toFlagWriteResponse(flag)})
}

// HandleToggle handles POST /{key}/toggle — toggles the enabled state.
func (h *Handler) HandleToggle(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	flag, err := h.svc.Toggle(r.Context(), key, req.Enabled)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toFlagWriteResponse(flag)})
}

// HandleDelete handles DELETE /{key} — deletes a feature flag.
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	if err := h.svc.Delete(r.Context(), key); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RegisterRoutes registers flagwrite routes on a mux with admin-only policies.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("POST /", auth.Secured(h.HandleCreate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("PUT /{key}", auth.Secured(h.HandleUpdate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("POST /{key}/toggle", auth.Secured(h.HandleToggle, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("DELETE /{key}", auth.Secured(h.HandleDelete, auth.AnyRole(dto.RoleAdmin)))
}
