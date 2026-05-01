package websocket

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
	"github.com/google/uuid"
)

// UpgradeConfig configures the WebSocket upgrade handler.
type UpgradeConfig struct {
	// AllowedOrigins lists the origin patterns authorized for the upgrade.
	// Each entry must be an origin pattern in the form scheme://host[:port],
	// optionally with glob wildcards (e.g. "https://example.com",
	// "https://*.example.com", "http://*", "http://localhost:*"). Bare host
	// patterns (e.g. "example.com") are rejected because coder/websocket's
	// OriginPatterns matches against the request's Origin header, which
	// always carries a scheme — a bare host would never match a real
	// browser handshake and would silently disable origin checking.
	//
	// AllowedOrigins must be non-empty and must not contain the full
	// wildcard "*". The error-returning UpgradeHandler rejects invalid
	// configuration; the MustUpgradeHandler composition-root helper panics
	// on the same error. Validate normalizes whitespace in-place so that
	// the slice handed to coder/websocket is the exact one that passed
	// validation (no trim drift between check and runtime).
	AllowedOrigins []string
}

// Validate checks that the UpgradeConfig is well-formed and rewrites
// AllowedOrigins with the trimmed, validated patterns so the slice handed
// to coder/websocket.AcceptOptions.OriginPatterns is identical to the one
// that was checked. Pointer receiver is required so the rewrite is visible
// to the caller's local cfg copy inside UpgradeHandler.
func (c *UpgradeConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errcode.New(errcode.ErrWebsocketOriginsMissing,
			"websocket: UpgradeConfig.AllowedOrigins must be non-empty (fail-closed)")
	}
	normalized := make([]string, 0, len(c.AllowedOrigins))
	for _, origin := range c.AllowedOrigins {
		pattern := strings.TrimSpace(origin)
		if pattern == "" || pattern == "*" {
			return errcode.New(errcode.ErrWebsocketOriginsInvalid,
				"websocket: UpgradeConfig.AllowedOrigins must use explicit origin patterns (scheme://host); wildcard * is forbidden")
		}
		if !strings.Contains(pattern, "://") {
			return errcode.New(errcode.ErrWebsocketOriginsInvalid,
				"websocket: UpgradeConfig.AllowedOrigins entry "+strconv.Quote(pattern)+
					" must be an origin pattern with scheme (e.g. https://example.com, https://*.example.com, http://*); "+
					"bare host is rejected because coder/websocket OriginPatterns matches against the Origin header, which always carries a scheme")
		}
		normalized = append(normalized, pattern)
	}
	c.AllowedOrigins = normalized
	return nil
}

// UpgradeHandler returns an http.Handler that upgrades HTTP connections to
// WebSocket and registers them with the Hub. It rejects a nil hub or an
// invalid cfg at construction time — error-first fail-fast — so static-wiring
// mistakes surface at composition root instead of the first HTTP request
// (PR-MODE-6.1). SEC-FAIL-CLOSED: the previous behavior of silently setting
// InsecureSkipVerify=true for empty origins is removed.
func UpgradeHandler(hub *rtws.Hub, cfg UpgradeConfig) (http.Handler, error) {
	if hub == nil {
		return nil, errcode.New(errcode.ErrWebsocketHubMissing,
			"websocket: UpgradeHandler hub must not be nil (fail-fast at wire time)")
	}
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

		remoteAddr := safeAddr(r.RemoteAddr)
		if regErr := hub.Register(r.Context(), conn); regErr != nil {
			_ = wsConn.Close(websocket.StatusNormalClosure, "registration rejected")
			slog.Warn("websocket: register rejected",
				slog.Any("error", regErr),
				slog.String("remote_addr", remoteAddr),
			)
			return
		}
		slog.Info("websocket: client connected",
			slog.String("conn_id", connID),
			slog.String("remote_addr", remoteAddr),
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
	addr := safeAddr(r.RemoteAddr)
	slog.Error("websocket: upgrade failed",
		slog.Any("error", err),
		slog.String("remote_addr", addr),
	)
}

// safeAddr sanitizes a network address string for use in structured log fields,
// preventing log-injection via malformed values (gosec G706). It parses host:port
// with net.SplitHostPort; on failure it strips control characters from the raw value.
func safeAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Fallback: strip control characters from the raw value.
		return strings.Map(func(c rune) rune {
			if c < 0x20 || c == 0x7F {
				return -1
			}
			return c
		}, addr)
	}
	return net.JoinHostPort(host, port)
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
