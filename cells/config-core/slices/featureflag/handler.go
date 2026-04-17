package featureflag

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
)

// FeatureFlagResponse is the public DTO for FeatureFlag, isolating the API
// contract from the domain model.
type FeatureFlagResponse struct {
	ID                string `json:"id"`
	Key               string `json:"key"`
	Type              string `json:"type"`
	Enabled           bool   `json:"enabled"`
	RolloutPercentage int    `json:"rolloutPercentage"`
}

func toFeatureFlagResponse(f *domain.FeatureFlag) FeatureFlagResponse {
	if f == nil {
		return FeatureFlagResponse{}
	}
	return FeatureFlagResponse{
		ID: f.ID, Key: f.Key, Type: string(f.Type),
		Enabled: f.Enabled, RolloutPercentage: f.RolloutPercentage,
	}
}

// EvaluateResultResponse is the public DTO for EvaluateResult, isolating the
// API contract from the service-layer model.
type EvaluateResultResponse struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

func toEvaluateResultResponse(r *EvaluateResult) EvaluateResultResponse {
	if r == nil {
		return EvaluateResultResponse{}
	}
	return EvaluateResultResponse{Key: r.Key, Enabled: r.Enabled}
}

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
		slog.Warn("pagination: request validation failed",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path),
		)
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	result, err := h.svc.List(r.Context(), pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toFeatureFlagResponse))
}

// HandleGet handles GET /{key} — returns a single feature flag.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	flag, err := h.svc.GetByKey(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toFeatureFlagResponse(flag)})
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

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toEvaluateResultResponse(result)})
}
