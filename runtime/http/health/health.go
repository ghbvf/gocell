// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints. /readyz returns aggregate readiness by default and only exposes
// detailed cell and dependency breakdown in verbose mode.
package health

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/assembly"
)

// Checker is a named readiness probe. Returning a non-nil error marks the
// check as unhealthy.
type Checker func() error

// VerboseTokenHeader is the HTTP header used to authenticate /readyz?verbose
// requests when a verbose token is configured via SetVerboseToken.
const VerboseTokenHeader = "X-Readyz-Token"

// Handler exposes /healthz and /readyz endpoints.
type Handler struct {
	assembly *assembly.CoreAssembly

	mu           sync.RWMutex
	checkers     map[string]Checker
	adapterInfo  map[string]string // static adapter metadata for verbose output
	verboseToken string            // if non-empty, require this token for verbose output
	shuttingDown atomic.Bool
}

// New creates a Handler backed by the given CoreAssembly.
func New(asm *assembly.CoreAssembly) *Handler {
	return &Handler{
		assembly: asm,
		checkers: make(map[string]Checker),
	}
}

// RegisterChecker adds a named readiness checker. It panics if a checker with
// the same name is already registered (fail-fast at startup, matching Go
// convention for registration functions like http.HandleFunc).
func (h *Handler) RegisterChecker(name string, fn Checker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.checkers[name]; exists {
		panic(fmt.Sprintf("health: duplicate checker name %q", name))
	}
	h.checkers[name] = fn
}

// SetVerboseToken sets a bearer token that must be provided via the
// X-Readyz-Token header to access /readyz?verbose output. When empty (default),
// verbose mode is unrestricted for backward compatibility.
//
// ref: Kubernetes withholds error reasons in verbose output but exposes check
// names. GoCell goes further: the entire verbose block (cell names, dependency
// names) is gated behind a token when configured.
func (h *Handler) SetVerboseToken(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.verboseToken = token
}

// SetAdapterInfo sets static adapter metadata that is included in /readyz
// verbose output. Helps operators verify which storage/bus backends are active.
func (h *Handler) SetAdapterInfo(info map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.adapterInfo = info
}

// SetShuttingDown marks the handler as shutting down. Once called,
// ReadyzHandler always returns 503 regardless of checker results.
// This enables load balancers to stop sending traffic before the
// HTTP server closes connections.
//
// Intended for framework use only (called by bootstrap.Run during shutdown).
func (h *Handler) SetShuttingDown() {
	h.shuttingDown.Store(true)
}

// LivezHandler returns an http.HandlerFunc for the /healthz liveness endpoint.
// Liveness is process-level: if the handler can serve a response, the process
// is alive. Readiness details belong to /readyz.
func (h *Handler) LivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "healthy",
		})
	}
}

// ReadyzHandler returns an http.HandlerFunc for the /readyz readiness endpoint.
// It runs all registered readiness checkers in addition to the Cell health.
// By default it returns only aggregate readiness status. Detailed cell and
// dependency breakdown is returned only when the request enables verbose mode.
//
// Security: verbose=true exposes internal topology (cell names, dependency
// names). Use SetVerboseToken to require an X-Readyz-Token header for verbose
// access, or restrict ?verbose at the ingress layer.
func (h *Handler) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.shuttingDown.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "shutting_down",
			})
			return
		}
		verbose := h.verboseAllowed(r)
		allHealthy, cells := h.aggregateCellHealth(verbose)
		checkersCopy := h.copyCheckers()
		depHealthy, dependencies := h.aggregateCheckerHealth(checkersCopy, verbose)
		if !depHealthy {
			allHealthy = false
		}

		status := "healthy"
		httpStatus := http.StatusOK
		if !allHealthy {
			status = "unhealthy"
			httpStatus = http.StatusServiceUnavailable
		}
		response := map[string]any{"status": status}
		if verbose {
			response["cells"] = cells
			response["dependencies"] = dependencies
			h.mu.RLock()
			if h.adapterInfo != nil {
				response["adapters"] = h.adapterInfo
			}
			h.mu.RUnlock()
		}
		writeJSON(w, httpStatus, response)
	}
}

// aggregateCellHealth checks all cell health statuses. When verbose is true it
// also builds a name→status map for the response body.
func (h *Handler) aggregateCellHealth(verbose bool) (allHealthy bool, cells map[string]string) {
	allHealthy = true
	cellHealth := h.assembly.Health()
	if verbose {
		cells = make(map[string]string, len(cellHealth))
	}
	for id, hs := range cellHealth {
		if verbose {
			cells[id] = hs.Status
		}
		if hs.Status != "healthy" {
			allHealthy = false
		}
	}
	return allHealthy, cells
}

// copyCheckers returns a snapshot of the registered checkers under a read lock.
func (h *Handler) copyCheckers() map[string]Checker {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cp := make(map[string]Checker, len(h.checkers))
	for k, v := range h.checkers {
		cp[k] = v
	}
	return cp
}

// aggregateCheckerHealth runs all checkers. When verbose is true it also
// builds a name→status map for the response body.
func (h *Handler) aggregateCheckerHealth(checkers map[string]Checker, verbose bool) (allHealthy bool, deps map[string]string) {
	allHealthy = true
	if verbose {
		deps = make(map[string]string, len(checkers))
	}
	for name, fn := range checkers {
		status := "healthy"
		if err := fn(); err != nil {
			status = "unhealthy"
			allHealthy = false
		}
		if verbose {
			deps[name] = status
		}
	}
	return allHealthy, deps
}

// verboseAllowed returns true when the request is allowed to see verbose output.
// When a verbose token is configured, the request must include a matching
// X-Readyz-Token header in addition to the ?verbose query parameter.
func (h *Handler) verboseAllowed(r *http.Request) bool {
	if !readyzVerbose(r) {
		return false
	}
	h.mu.RLock()
	token := h.verboseToken
	h.mu.RUnlock()
	if token == "" {
		return true // no token configured — backward compatible
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(VerboseTokenHeader)), []byte(token)) == 1 {
		return true
	}
	// Token configured but request missing/mismatched. Warn so probing
	// attempts are observable; don't Error since the request still succeeds
	// (just without verbose output) and the endpoint is operating as designed.
	slog.Warn("readyz: verbose token mismatch; suppressing verbose output",
		slog.String("remote_addr", r.RemoteAddr))
	return false
}

// readyzVerbose returns true when the request opts in to detailed output.
// Accepted forms: ?verbose, ?verbose=, ?verbose=1, ?verbose=true.
// All other values (false, yes, debug, …) are treated as non-verbose.
func readyzVerbose(r *http.Request) bool {
	values, ok := r.URL.Query()["verbose"]
	if !ok {
		return false
	}
	// url.ParseQuery always yields at least [""] when the key is present,
	// so we iterate values directly without a separate len==0 guard.
	for _, value := range values {
		normalized := strings.TrimSpace(strings.ToLower(value))
		switch normalized {
		case "", "1", "true":
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("health: failed to write response", slog.String("error", err.Error()))
	}
}
