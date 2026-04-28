package websocket

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
	"github.com/google/uuid"
)

// UpgradeConfig configures the WebSocket upgrade handler.
type UpgradeConfig struct {
	// AllowedOrigins is a list of allowed origin patterns for the upgrade.
	// It must be non-empty and must not contain the full wildcard "*". The
	// error-returning UpgradeHandler rejects invalid configuration; the
	// MustUpgradeHandler composition-root helper panics on the same error.
	AllowedOrigins []string
}

// Validate checks that the UpgradeConfig is well-formed. Returns an errcode
// error if AllowedOrigins is empty so callers can distinguish configuration
// errors from runtime errors.
func (c UpgradeConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errcode.New(errcode.ErrWebsocketOriginsMissing,
			"websocket: UpgradeConfig.AllowedOrigins must be non-empty (fail-closed)")
	}
	for _, origin := range c.AllowedOrigins {
		if pattern := strings.TrimSpace(origin); pattern == "" || pattern == "*" {
			return errcode.New(errcode.ErrWebsocketOriginsInvalid,
				"websocket: UpgradeConfig.AllowedOrigins must use explicit host patterns; wildcard * is forbidden")
		}
	}
	return nil
}

// UpgradeHandler returns an http.Handler that upgrades HTTP connections to
// WebSocket and registers them with the Hub. It rejects an empty
// cfg.AllowedOrigins at construction time — SEC-FAIL-CLOSED: the previous
// behaviour of silently setting InsecureSkipVerify=true for empty origins is
// removed.
func UpgradeHandler(hub *rtws.Hub, cfg UpgradeConfig) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hub.IsRunning() {
			http.Error(w, "websocket hub not ready", http.StatusServiceUnavailable)
			return
		}

		opts := &websocket.AcceptOptions{
			OriginPatterns: cfg.AllowedOrigins,
		}

		if _, ok := w.(http.Hijacker); !ok {
			logUpgradeFailure(r, errcode.New(ErrAdapterWSUpgrade,
				"websocket: response writer does not support hijack"))
			http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
			return
		}

		acceptWriter := newUpgradeAcceptWriter(w)
		wsConn, err := websocket.Accept(acceptWriter, r, opts)
		if err != nil {
			logUpgradeFailure(r, err)
			http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
			return
		}

		wsConn.SetReadLimit(hub.Config().ReadLimit)

		connID := "ws-" + uuid.NewString()
		conn := NewConn(connID, wsConn)

		if regErr := hub.Register(r.Context(), conn); regErr != nil {
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
	}), nil
}

// MustUpgradeHandler is the static-wiring variant of UpgradeHandler.
func MustUpgradeHandler(hub *rtws.Hub, cfg UpgradeConfig) http.Handler {
	handler, err := UpgradeHandler(hub, cfg)
	if err != nil {
		panic(err)
	}
	return handler
}

func logUpgradeFailure(r *http.Request, err error) {
	slog.Error("websocket: upgrade failed",
		slog.Any("error", err),
		slog.String("remote_addr", r.RemoteAddr),
	)
}

type upgradeAcceptWriter struct {
	http.ResponseWriter
	header http.Header
	status int
}

func newUpgradeAcceptWriter(w http.ResponseWriter) *upgradeAcceptWriter {
	return &upgradeAcceptWriter{
		ResponseWriter: w,
		header:         make(http.Header),
	}
}

func (w *upgradeAcceptWriter) Header() http.Header {
	return w.header
}

func (w *upgradeAcceptWriter) WriteHeader(status int) {
	w.status = status
	if status == http.StatusSwitchingProtocols {
		copyHeaders(w.ResponseWriter.Header(), w.header)
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *upgradeAcceptWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}

func (w *upgradeAcceptWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errcode.New(ErrAdapterWSUpgrade,
			"websocket: response writer does not support hijack")
	}
	copyHeaders(w.ResponseWriter.Header(), w.header)
	return hijacker.Hijack()
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
