package sessionrefresh

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/access-core/internal/dto"
	"github.com/ghbvf/gocell/pkg/httputil"
)

func toTokenPairResponse(p *TokenPair) dto.TokenPairResponse {
	if p == nil {
		return dto.TokenPairResponse{}
	}
	return dto.TokenPairResponse{
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		ExpiresAt:    p.ExpiresAt,
	}
}

// Handler provides HTTP endpoints for session refresh.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-refresh Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleRefresh handles POST /api/v1/access/sessions/refresh.
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	pair, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toTokenPairResponse(pair)})
}
