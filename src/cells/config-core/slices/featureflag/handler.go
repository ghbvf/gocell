package featureflag

import (
	"encoding/json"
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for feature flag operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a feature-flag Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleList handles GET / — returns all feature flags.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	flags, err := h.svc.List(r.Context())
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": flags, "total": len(flags)})
}

// HandleGet handles GET /{key} — returns a single feature flag.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	flag, err := h.svc.GetByKey(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": flag})
}

// HandleEvaluate handles POST /{key}/evaluate — evaluates a feature flag.
func (h *Handler) HandleEvaluate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "ERR_VALIDATION_REQUIRED_FIELD", "invalid request body")
		return
	}

	result, err := h.svc.Evaluate(r.Context(), key, req.Subject)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": result})
}
