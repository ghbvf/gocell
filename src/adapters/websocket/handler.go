package websocket

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/id"
	"nhooyr.io/websocket"
)

// UserIDFromRequest is a function that extracts the user ID from an HTTP
// request. The caller injects the implementation (e.g. reading a JWT claim
// from context set by auth middleware).
type UserIDFromRequest func(r *http.Request) string

// HandlerConfig configures the UpgradeHandler.
type HandlerConfig struct {
	// AllowedOrigins restricts the Origin header during the WebSocket
	// handshake. An empty slice allows all origins (development only).
	AllowedOrigins []string
	// UserIDExtractor extracts the user ID from the request.
	// If nil, every connection gets an empty userID.
	UserIDExtractor UserIDFromRequest
}

// UpgradeHandler returns an http.Handler that upgrades incoming HTTP requests
// to WebSocket connections and registers them with the given Hub.
//
// Origin checking: when HandlerConfig.AllowedOrigins is non-empty the handler
// validates the request Origin header against the allowlist. An empty list
// disables origin checking (suitable for development).
func UpgradeHandler(hub *Hub, cfg HandlerConfig) http.Handler {
	acceptOpts := &websocket.AcceptOptions{}
	if len(cfg.AllowedOrigins) > 0 {
		acceptOpts.OriginPatterns = cfg.AllowedOrigins
	} else {
		acceptOpts.InsecureSkipVerify = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, acceptOpts)
		if err != nil {
			slog.Error("websocket: upgrade failed",
				"error", err,
				"remote_addr", r.RemoteAddr,
			)
			// Accept already wrote the HTTP error response.
			return
		}

		connID := id.New("ws")
		var userID string
		if cfg.UserIDExtractor != nil {
			userID = cfg.UserIDExtractor(r)
		}

		c := &conn{
			id:     connID,
			userID: userID,
			ws:     ws,
			msgs:   make(chan []byte, hub.cfg.SendBuffer),
		}

		hub.serve(r.Context(), c)
	})
}

// WriteSignal is a convenience helper that serialises a signal-first JSON
// payload and broadcasts it. Example signal:
//
//	{"type":"refresh","resource":"config"}
//
// The caller is responsible for marshalling the JSON; this function simply
// delegates to Hub.Broadcast.
func WriteSignal(hub *Hub, signal []byte) error {
	if hub == nil {
		return errcode.New(ErrAdapterWSSend, "hub is nil")
	}
	hub.Broadcast(signal)
	return nil
}
