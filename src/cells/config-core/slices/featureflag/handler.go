package featureflag

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

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

// Routes returns a chi.Router with feature-flag routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.handleList)
	r.Get("/{key}", h.handleGet)
	r.Post("/{key}/evaluate", h.handleEvaluate)
	return r
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	flags, err := h.svc.List(r.Context())
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": flags, "total": len(flags)})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	flag, err := h.svc.GetByKey(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": flag})
}

func (h *Handler) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

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
