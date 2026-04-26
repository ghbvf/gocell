package websocket

import (
	"log/slog"
	"net/http"

	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
	"github.com/google/uuid"
)

// UpgradeConfig configures the WebSocket upgrade handler.
type UpgradeConfig struct {
	// AllowedOrigins is a list of allowed origin patterns for the upgrade.
	// Must be non-empty — the handler is fail-closed and panics at construction
	// time if AllowedOrigins is nil or empty. Use []string{"*"} only in
	// development environments where origin checking is intentionally disabled.
	AllowedOrigins []string
}

// Validate checks that the UpgradeConfig is well-formed. Returns an errcode
// error if AllowedOrigins is empty so callers can distinguish configuration
// errors from runtime errors.
func (c UpgradeConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errcode.New(errcode.ErrWebsocketOriginsMissing,
			"websocket: UpgradeConfig.AllowedOrigins must be non-empty; use [\"*\"] only in dev (fail-closed)")
	}
	return nil
}

// UpgradeHandler returns an http.Handler that upgrades HTTP connections to
// WebSocket and registers them with the Hub. Panics at construction time if
// cfg.AllowedOrigins is empty — SEC-FAIL-CLOSED: the previous behaviour of
// silently setting InsecureSkipVerify=true for empty origins is removed.
// Callers at the composition root will observe the panic immediately at
// startup rather than silently accepting connections from all origins.
func UpgradeHandler(hub *rtws.Hub, cfg UpgradeConfig) http.Handler {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hub.IsRunning() {
			http.Error(w, "websocket hub not ready", http.StatusServiceUnavailable)
			return
		}

		opts := &websocket.AcceptOptions{
			OriginPatterns: cfg.AllowedOrigins,
		}

		wsConn, err := websocket.Accept(w, r, opts)
		if err != nil {
			slog.Error("websocket: upgrade failed",
				slog.Any("error", err),
				slog.String("remote_addr", r.RemoteAddr),
			)
			httpErr := errcode.Wrap(ErrAdapterWSUpgrade, "websocket: upgrade failed", err)
			http.Error(w, httpErr.Error(), http.StatusBadRequest)
			return
		}

		wsConn.SetReadLimit(hub.Config().ReadLimit)

		connID := "ws-" + uuid.NewString()
		conn := NewConn(connID, wsConn)

		if regErr := hub.Register(conn); regErr != nil {
			_ = wsConn.Close(websocket.StatusNormalClosure, "registration rejected")
			slog.Warn("websocket: register rejected",
				slog.Any("error", regErr),
				slog.String("remote_addr", r.RemoteAddr),
			)
			return
		}
		slog.Info("websocket: client connected",
			slog.String("conn_id", connID),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}
