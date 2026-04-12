package featureflag

import (
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

// HandleList handles GET / — returns paginated feature flags.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	pageReq, err := httputil.ParsePageRequest(r)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	result, err := h.svc.List(r.Context(), pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data":       result.Items,
		"nextCursor": result.NextCursor,
		"hasMore":    result.HasMore,
	})
}

// HandleGet handles GET /{key} — returns a single feature flag.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	flag, err := h.svc.GetByKey(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
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
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	result, err := h.svc.Evaluate(r.Context(), key, req.Subject)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": result})
}
