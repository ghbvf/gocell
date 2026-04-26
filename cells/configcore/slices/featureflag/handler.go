package featureflag

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	dto "github.com/ghbvf/gocell/cells/configcore/internal/dto"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// FeatureFlagResponse is the public DTO for FeatureFlag, isolating the API
// contract from the domain model.
type FeatureFlagResponse struct {
	ID                string    `json:"id"`
	Key               string    `json:"key"`
	Type              string    `json:"type"`
	Enabled           bool      `json:"enabled"`
	RolloutPercentage int       `json:"rolloutPercentage"`
	Description       string    `json:"description"`
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func toFeatureFlagResponse(f *domain.FeatureFlag) FeatureFlagResponse {
	if f == nil {
		return FeatureFlagResponse{}
	}
	return FeatureFlagResponse{
		ID: f.ID, Key: f.Key, Type: string(f.Type),
		Enabled: f.Enabled, RolloutPercentage: f.RolloutPercentage,
		Description: f.Description, Version: f.Version,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
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

// spec vars for featureflag routes, cross-checked against
// contracts/http/config/flags/*/v1/contract.yaml by FMT-18.
var (
	specFlagsList = wrapper.ContractSpec{
		ID: "http.config.flags.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/flags/",
	}
	specFlagsGet = wrapper.ContractSpec{
		ID: "http.config.flags.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/flags/{key}",
	}
	specFlagsEvaluate = wrapper.ContractSpec{
		ID: "http.config.flags.evaluate.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/flags/{key}/evaluate",
	}
)

// Handler provides HTTP endpoints for feature flag operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a feature-flag Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers feature-flag read routes on mux via auth.Mount so
// CH-04/CH-05 governance can correlate contracts to handler functions.
// All routes are admin-gated (auth.AnyRole(RoleAdmin)).
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract: specFlagsList,
		Handler:  http.HandlerFunc(h.HandleList),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
	auth.Mount(mux, auth.Route{
		Contract: specFlagsGet,
		Handler:  http.HandlerFunc(h.HandleGet),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
	auth.Mount(mux, auth.Route{
		Contract: specFlagsEvaluate,
		Handler:  http.HandlerFunc(h.HandleEvaluate),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
}

// HandleList handles GET / — returns paginated feature flags.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	pageReq, ok := httputil.ParsePageParamsOrWrite(w, r)
	if !ok {
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
