package websocket

import (
	"context"
	"log/slog"
	"net/http"

	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
)

// UpgradeConfig configures the WebSocket upgrade handler.
type UpgradeConfig struct {
	// AllowedOrigins is a list of allowed origin patterns for the upgrade.
	// An empty list allows all origins (insecure, for development only).
	AllowedOrigins []string
	// ReadLimit is the maximum message size in bytes. Default: 64KB.
	ReadLimit int64
}

// UpgradeHandler returns an http.Handler that upgrades HTTP connections to
// WebSocket and registers them with the Hub.
func UpgradeHandler(hub *rtws.Hub, cfg UpgradeConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opts := &websocket.AcceptOptions{}

		if len(cfg.AllowedOrigins) > 0 {
			opts.OriginPatterns = cfg.AllowedOrigins
		} else {
			opts.InsecureSkipVerify = true
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

		if cfg.ReadLimit > 0 {
			wsConn.SetReadLimit(cfg.ReadLimit)
		} else {
			wsConn.SetReadLimit(64 * 1024) // 64KB default
		}

		connID := "ws-" + uuid.NewString()
		conn := NewConn(connID, wsConn)

		hub.Register(context.Background(), conn)
		slog.Info("websocket: client connected",
			slog.String("conn_id", connID),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}
