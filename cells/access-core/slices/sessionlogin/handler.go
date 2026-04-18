package sessionlogin

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
		AccessToken:           p.AccessToken,
		RefreshToken:          p.RefreshToken,
		ExpiresAt:             p.ExpiresAt,
		SessionID:             p.SessionID,
		PasswordResetRequired: p.PasswordResetRequired,
	}
}

// Handler provides HTTP endpoints for session login.
type Handler struct {
	svc *Service
}

// NewHandler creates a session-login Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleLogin handles POST /api/v1/access/sessions/login.
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	pair, err := h.svc.Login(r.Context(), LoginInput{
		Username: req.Username, Password: req.Password,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toTokenPairResponse(pair)})
}
