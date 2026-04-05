package configwrite

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for config write operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a config-write Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes returns a chi.Router with config-write routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.HandleCreate)
	r.Put("/{key}", h.HandleUpdate)
	r.Delete("/{key}", h.HandleDelete)
	return r
}

// HandleCreate handles POST / — creates a new config entry.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "ERR_VALIDATION_REQUIRED_FIELD", "invalid request body")
		return
	}

	entry, err := h.svc.Create(r.Context(), CreateInput{Key: req.Key, Value: req.Value})
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": entry})
}

// HandleUpdate handles PUT /{key} — updates an existing config entry.
func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "ERR_VALIDATION_REQUIRED_FIELD", "invalid request body")
		return
	}

	entry, err := h.svc.Update(r.Context(), UpdateInput{Key: key, Value: req.Value})
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": entry})
}

// HandleDelete handles DELETE /{key} — deletes a config entry.
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	if err := h.svc.Delete(r.Context(), key); err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
