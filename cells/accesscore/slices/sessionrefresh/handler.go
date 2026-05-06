package sessionrefresh

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	refreshgen "github.com/ghbvf/gocell/generated/contracts/http/auth/refresh/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
)

// RefreshAdapter implements refreshgen.Service for http.auth.refresh.v1.
// It adapts the slice-internal Service (Refresh takes a raw token string)
// to the generated interface (Refresh takes *refreshgen.Request).
type RefreshAdapter struct{ S *Service }

// Refresh implements refreshgen.Service. The generated handler already validates
// and decodes refreshToken from the request body.
func (a RefreshAdapter) Refresh(ctx context.Context, req *refreshgen.Request) (refreshgen.RefreshResponseObject, error) {
	pair, err := a.S.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return nil, err
	}
	return refreshgen.Refresh200JSONResponse{
		Data: toRefreshResponseData(pair),
	}, nil
}

// toRefreshResponseData converts an internal TokenPair to the generated contract DTO.
func toRefreshResponseData(p dto.TokenPair) *refreshgen.ResponseData {
	return &refreshgen.ResponseData{
		AccessToken:           p.AccessToken,
		RefreshToken:          p.RefreshToken,
		ExpiresAt:             p.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		SessionId:             p.SessionID,
		UserId:                p.UserID,
		PasswordResetRequired: p.PasswordResetRequired,
	}
}

// Handler is the route handler for the sessionrefresh slice.
// The generated handler emits Public:true so no JWT is required for this route.
type Handler struct {
	refreshH *refreshgen.Handler
}

// NewHandler creates a sessionrefresh Handler using the generated refresh handler.
// No policy argument: the refresh endpoint is Public (no JWT required).
func NewHandler(svc *Service) *Handler {
	return &Handler{
		refreshH: refreshgen.NewHandler(RefreshAdapter{svc}),
	}
}

// RegisterRoutes mounts the refresh contract handler on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.refreshH.RegisterRoutes(mux)
}
