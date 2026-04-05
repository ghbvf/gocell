package websocket

import (
	"context"
	"log/slog"
	"net/http"

	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// UpgradeConfig configures the WebSocket upgrade handler.
type UpgradeConfig struct {
	// AllowedOrigins is a list of allowed origin patterns for the upgrade.
	// An empty list allows all origins (insecure, for development only).
	AllowedOrigins []string
}

// UpgradeHandler returns an http.Handler that upgrades HTTP connections to
// WebSocket and registers them with the Hub.
func UpgradeHandler(hub *Hub, cfg UpgradeConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opts := &websocket.AcceptOptions{}

		if len(cfg.AllowedOrigins) > 0 {
			opts.OriginPatterns = cfg.AllowedOrigins
		} else {
			// Development mode: skip origin verification.
			opts.InsecureSkipVerify = true
		}

		conn, err := websocket.Accept(w, r, opts)
		if err != nil {
			slog.Error("websocket: upgrade failed",
				slog.Any("error", err),
				slog.String("remote_addr", r.RemoteAddr),
			)
			httpErr := errcode.Wrap(ErrAdapterWSUpgrade, "websocket: upgrade failed", err)
			http.Error(w, httpErr.Error(), http.StatusBadRequest)
			return
		}

		// Use a detached context for the WebSocket lifecycle. The HTTP
		// request context is cancelled when this handler returns, but the
		// WebSocket connection outlives the HTTP handler.
		connID := hub.Register(context.Background(), conn)
		slog.Info("websocket: client connected",
			slog.String("conn_id", connID),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}
