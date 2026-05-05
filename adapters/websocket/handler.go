package websocket

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/logutil"
	"github.com/ghbvf/gocell/runtime/auth"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
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

	// Authenticator validates the HTTP request before WebSocket upgrade.
	// Required (nil → ErrWebsocketAuthenticatorMissing at construction).
	//
	// The Authenticator runs BEFORE websocket.Accept; rejection writes a
	// plain-text 401 directly to the response writer (browser WebSocket APIs
	// cannot read the response body, so envelope JSON is meaningless).
	//
	// Composition root selects one of:
	//   - auth.NewJWTAuthenticator(verifier)        — token-via-Authorization-header
	//   - auth.NewContextAuthenticator()            — already authenticated by listener middleware
	//   - auth.NewAnonymousAuthenticator()          — explicit unauthenticated channel
	//   - custom auth.AuthenticatorFunc             — query-param / cookie / subprotocol token
	//
	// ref: coder/websocket accept.go — http.Error(w, err.Error(), code) before Accept
	Authenticator auth.Authenticator
}

// Validate checks that the UpgradeConfig is well-formed and rewrites
// AllowedOrigins with the trimmed, validated patterns so the slice handed
// to coder/websocket.AcceptOptions.OriginPatterns is identical to the one
// that was checked. Pointer receiver is required so the rewrite is visible
// to the caller's local cfg copy inside UpgradeHandler.
func (c *UpgradeConfig) Validate() error {
	if len(c.AllowedOrigins) == 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrWebsocketOriginsMissing,
			"websocket: UpgradeConfig.AllowedOrigins must be non-empty (fail-closed)")
	}
	normalized := make([]string, 0, len(c.AllowedOrigins))
	for _, origin := range c.AllowedOrigins {
		pattern := strings.TrimSpace(origin)
		if pattern == "" || pattern == "*" {
			return errcode.New(errcode.KindInternal, errcode.ErrWebsocketOriginsInvalid,
				"websocket: UpgradeConfig.AllowedOrigins must use explicit origin patterns (scheme://host); wildcard * is forbidden")
		}
		if !strings.Contains(pattern, "://") {
			return errcode.New(errcode.KindInternal, errcode.ErrWebsocketOriginsInvalid,
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
		return nil, errcode.New(errcode.KindInternal, errcode.ErrWebsocketHubMissing,
			"websocket: UpgradeHandler hub must not be nil (fail-fast at wire time)")
	}
	if cfg.Authenticator == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrWebsocketAuthenticatorMissing,
			"websocket: UpgradeHandler Authenticator must not be nil (SEC-FAIL-CLOSED); "+
				"use auth.NewAnonymousAuthenticator() for explicit unauthenticated endpoints")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hub.IsRunning() {
			http.Error(w, "websocket hub not ready", http.StatusServiceUnavailable)
			return
		}
		principal, ok := authenticateForUpgrade(w, r, cfg.Authenticator)
		if !ok {
			return
		}
		if _, ok := w.(http.Hijacker); !ok {
			logUpgradeFailure(r, errcode.New(errcode.KindInternal, ErrAdapterWSUpgrade,
				"websocket: response writer does not support hijack"))
			http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
			return
		}
		acceptUpgradeAndRegister(w, r, hub, cfg, principal)
	}), nil
}

// authenticateForUpgrade runs cfg.Authenticator on the request. On absent or
// invalid credentials it writes a plain-text 401 response and returns false;
// the caller (UpgradeHandler) then short-circuits without calling
// websocket.Accept. cf. coder/websocket accept.go: HTTP errors are returned
// before Accept consumes the handshake bytes.
func authenticateForUpgrade(w http.ResponseWriter, r *http.Request, a auth.Authenticator) (*auth.Principal, bool) {
	principal, ok, err := a.Authenticate(r)
	if err != nil {
		slog.Warn("websocket: upgrade rejected — invalid credential",
			slog.Any("error", err),
			slog.String("remote_addr", logutil.SafeAddr(r.RemoteAddr)),
			slog.String("path", r.URL.Path),
			slog.String("error_code", string(errcode.ErrWebsocketUpgradeUnauthenticated)),
		)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	if !ok || principal == nil {
		slog.Warn("websocket: upgrade rejected — credential absent",
			slog.String("remote_addr", logutil.SafeAddr(r.RemoteAddr)),
			slog.String("path", r.URL.Path),
			slog.String("error_code", string(errcode.ErrWebsocketUpgradeUnauthenticated)),
		)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return principal, true
}

// acceptUpgradeAndRegister performs the WebSocket upgrade and hub registration
// after authentication has already succeeded. principal is bound to the
// resulting Conn.
func acceptUpgradeAndRegister(w http.ResponseWriter, r *http.Request, hub *rtws.Hub, cfg UpgradeConfig, principal *auth.Principal) {
	opts := &websocket.AcceptOptions{OriginPatterns: cfg.AllowedOrigins}
	acceptWriter := newUpgradeAcceptWriter(w)
	wsConn, err := websocket.Accept(acceptWriter, r, opts)
	if err != nil {
		logUpgradeFailure(r, err)
		http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
		return
	}
	wsConn.SetReadLimit(hub.Config().ReadLimit)

	connID := "ws-" + uuid.NewString()
	conn := NewConn(connID, principal, wsConn)

	remoteAddr := logutil.SafeAddr(r.RemoteAddr)
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
		slog.String("subject", principal.Subject),
		slog.String("kind", principal.Kind.String()),
	)
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
	addr := logutil.SafeAddr(r.RemoteAddr)
	msg := "websocket: upgrade failed"
	fields := []any{
		slog.Any("error", err),
		slog.String("remote_addr", addr),
	}
	if isClientUpgradeError(err) {
		slog.Warn(msg, fields...)
		return
	}
	slog.Error(msg, fields...)
}

// isClientUpgradeError reports whether the upgrade failure originated from
// the client (bad handshake, missing headers, protocol violation). Server-side
// failures (hijack not supported, internal write errors) stay at Error level.
func isClientUpgradeError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// coder/websocket Accept returns errors with these substrings for client-side issues.
	for _, marker := range []string{
		"websocket protocol violation",
		"Sec-WebSocket-Key",
		"Sec-WebSocket-Version",
		"expected GET method",
		"request must contain",
		"request method must be",
		"Origin",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
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
		return nil, nil, errcode.New(errcode.KindInternal, ErrAdapterWSUpgrade,
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
