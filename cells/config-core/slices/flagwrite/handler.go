package flagwrite

import (
	"net/http"
	"strings"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// HandleUpdate handles PUT /{key} — full replacement of a feature flag's
// mutable state. All three fields (enabled, rolloutPercentage, description)
// are required; the partial "toggle just enabled" workflow lives on
// POST /{key}/toggle. Using pointer fields lets us distinguish "caller
// omitted the field" from "caller sent the zero value" — the previous
// value-field decoder silently accepted an omitted enabled as false and
// could flip a live flag off.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apimachinery/pkg/runtime/
// serializer/json/json.go — strict decode + required-field enforcement at
// the decode boundary, before the object reaches the handler logic.
func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Enabled           *bool   `json:"enabled"`
		RolloutPercentage *int    `json:"rolloutPercentage"`
		Description       *string `json:"description"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}
	if err := validateUpdateRequest(req.Enabled, req.RolloutPercentage, req.Description); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	flag, err := h.svc.Update(r.Context(), UpdateInput{
		Key:               key,
		Enabled:           *req.Enabled,
		RolloutPercentage: *req.RolloutPercentage,
		Description:       *req.Description,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toFlagWriteResponse(flag)})
}

// validateUpdateRequest enforces the PUT full-replacement contract at the
// handler boundary: all three fields must be provided by the caller. An
// omitted field returns ErrFlagInvalidInput so callers get a deterministic
// 400 rather than a silent zero-write in the storage layer.
func validateUpdateRequest(enabled *bool, rolloutPercentage *int, description *string) error {
	missing := make([]string, 0, 3)
	if enabled == nil {
		missing = append(missing, "enabled")
	}
	if rolloutPercentage == nil {
		missing = append(missing, "rolloutPercentage")
	}
	if description == nil {
		missing = append(missing, "description")
	}
	if len(missing) == 0 {
		return nil
	}
	return errcode.New(errcode.ErrFlagInvalidInput,
		"PUT /flags/{key} requires all fields; missing: "+strings.Join(missing, ", ")+
			" — use POST /flags/{key}/toggle for partial updates")
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
// The mux argument is the narrow cell.RouteHandler interface so this single
// declaration is used by production wiring (cells/config-core/cell.go via
// cell.RouteMux), contract tests (*http.ServeMux), and cell-level integration
// tests — all paths share the same auth.Secured wrappers. Any regression that
// omits the Secured wrapper would surface the same way in every path.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) {
	mux.Handle("POST /", auth.Secured(h.HandleCreate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("PUT /{key}", auth.Secured(h.HandleUpdate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("POST /{key}/toggle", auth.Secured(h.HandleToggle, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("DELETE /{key}", auth.Secured(h.HandleDelete, auth.AnyRole(dto.RoleAdmin)))
}
