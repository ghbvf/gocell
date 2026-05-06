package sessionlogin

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	logingen "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
)

// LoginAdapter implements logingen.Service for http.auth.login.v1.
// It adapts the slice-internal Service (Login takes LoginInput) to the
// generated interface (Login takes *logingen.Request).
type LoginAdapter struct{ S *Service }

// Login implements logingen.Service. The generated handler already validates
// and decodes username+password from the request body.
func (a LoginAdapter) Login(ctx context.Context, req *logingen.Request) (logingen.LoginResponseObject, error) {
	pair, err := a.S.Login(ctx, LoginInput{
		Username: req.Username,
		Password: req.Password,
	})
	if err != nil {
		return nil, err
	}
	return logingen.Login201JSONResponse{
		Data: toLoginResponseData(pair),
	}, nil
}

// toLoginResponseData converts an internal TokenPair to the generated contract DTO.
func toLoginResponseData(p dto.TokenPair) *logingen.ResponseData {
	return &logingen.ResponseData{
		AccessToken:           p.AccessToken,
		RefreshToken:          p.RefreshToken,
		ExpiresAt:             p.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		SessionId:             p.SessionID,
		UserId:                p.UserID,
		PasswordResetRequired: p.PasswordResetRequired,
	}
}

// Handler is the route handler for the sessionlogin slice.
// The generated handler emits Public:true so no JWT is required for this route.
type Handler struct {
	loginH *logingen.Handler
}

// NewHandler creates a sessionlogin Handler using the generated login handler.
// No policy argument: the login endpoint is Public (no JWT required).
func NewHandler(svc *Service) *Handler {
	return &Handler{
		loginH: logingen.NewHandler(LoginAdapter{svc}),
	}
}

// RegisterRoutes mounts the login contract handler on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.loginH.RegisterRoutes(mux)
}
