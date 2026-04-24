// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints. /readyz returns aggregate readiness by default and only exposes
// detailed cell and dependency breakdown in verbose mode.
//
// PR-A35 made two structural guarantees:
//   - RegisterChecker wraps every checker with wrapCtxSafe so that the outer
//     Checker always returns as soon as ctx is cancelled, regardless of whether
//     the inner function cooperates. This removes the "uncooperative probe
//     leaks a goroutine past ReadyzHandler's return" trade-off that previously
//     sat at the aggregator level — the aggregator itself is now insulated
//     from inner-fn behaviour.
//   - /readyz requests are deduplicated via singleflight so that a burst of
//     concurrent probes shares one probe execution. This replaces the prior
//     plan of a fixed "max concurrent probes" semaphore (which required
//     picking a magic number) with a purely structural guard.
//
// ref: k8s.io/apiserver/pkg/server/healthz — readyz deadline + named probes.
// ref: uber-go/fx internal/lifecycle/lifecycle.go — ctx-aware lifecycle hooks.
// ref: golang.org/x/sync/singleflight — dedup concurrent duplicate calls.
package health

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// maxVerboseErrLen is the maximum length of a probe error string included in
// /readyz?verbose output. Error strings exceeding this limit are truncated
// with an ellipsis to bound response size and limit accidental exposure of
// long, potentially sensitive diagnostic messages.
const maxVerboseErrLen = 512

// singleflight keys for the two response shapes. Verbose and non-verbose
// results are not interchangeable (different body fields), so each shape gets
// its own key; concurrent requests of the same shape share one probe pass.
const (
	sfKeyAggregate = "readyz:aggregate"
	sfKeyVerbose   = "readyz:verbose"
)

// Checker is a named readiness probe. Returning a non-nil error marks the
// check as unhealthy. The context carries the deadline set on the Handler
// (default 5 s, matching Kubernetes readiness probe convention).
//
// RegisterChecker wraps supplied functions with wrapCtxSafe, so the effective
// Checker stored inside Handler honours ctx.Done regardless of the inner
// implementation's cooperativeness. See wrapCtxSafe for the full contract.
//
// ref: k8s.io/apiserver/pkg/server/healthz — HealthChecker interface with ctx.
type Checker = func(context.Context) error

// ProbeResult captures the outcome of a single readiness probe execution.
type ProbeResult struct {
	Status   string        // "healthy" | "unhealthy" | "timeout"
	Duration time.Duration // wall-clock time spent inside the probe
	// Err is exposed in /readyz?verbose output (truncated to maxVerboseErrLen).
	// Probe implementations MUST NOT include connection strings, tokens, or
	// other secrets in the error message.
	Err error // non-nil when Status != "healthy"
}

// Option configures a Handler.
type Option func(*Handler)

// WithDeadline sets a per-probe deadline for /readyz. All registered checkers
// must complete within this duration; checkers that exceed it are reported as
// status="timeout" and contribute to an unhealthy aggregate.
//
// Default is 5 s (Kubernetes readiness probe convention).
//
// ref: k8s.io/apiserver/pkg/server/healthz — server-side readyz timeout independent
// of the kubelet HTTP connection deadline.
func WithDeadline(d time.Duration) Option {
	return func(h *Handler) {
		if d > 0 {
			h.deadline = d
		}
	}
}

// WithVerboseDisabled declares that this Handler must never serve verbose
// output. Any request carrying ?verbose (with or without a token) is answered
// with the plain aggregate body — the verbose body and its token gate are
// inert. Intended for test harnesses and minimal assemblies that waive the
// verbose debug channel; production deployments should configure a verbose
// token instead so that operators can still reach verbose diagnostics.
func WithVerboseDisabled() Option {
	return func(h *Handler) {
		h.verboseDisabled = true
	}
}

// VerboseTokenHeader is the HTTP header used to authenticate /readyz?verbose
// requests. Verbose access always requires both a matching header and a
// pre-configured token (see SetVerboseToken); PR-A35 removed the prior
// "unconfigured = unrestricted" fallback.
const VerboseTokenHeader = "X-Readyz-Token"

// Handler exposes /healthz and /readyz endpoints.
type Handler struct {
	assembly *assembly.CoreAssembly

	// deadline is the per-probe timeout for /readyz. Default 5 s mirrors
	// Kubernetes readiness probe convention and is independent of the kubelet
	// HTTP connection deadline so that kubelet connection drops do not cancel
	// in-flight probes.
	deadline time.Duration

	// sf deduplicates concurrent /readyz executions so that a burst of
	// probes (e.g. kubelet + load balancer + manual curl) shares one probe
	// pass. This replaces a fixed semaphore: callers never see 503 "too many
	// probes" and there is no magic-number concurrency bound to tune.
	sf singleflight.Group

	mu              sync.RWMutex
	checkers        map[string]Checker
	adapterInfo     map[string]string // static adapter metadata for verbose output
	verboseToken    string            // required match for the X-Readyz-Token header; empty means verbose is denied
	verboseDisabled bool              // if true, /readyz?verbose is answered with the plain aggregate body
	shuttingDown    atomic.Bool
}

// New creates a Handler backed by the given CoreAssembly.
// The default probe deadline is 5 s (Kubernetes readiness probe convention).
func New(asm *assembly.CoreAssembly, opts ...Option) *Handler {
	h := &Handler{
		assembly: asm,
		checkers: make(map[string]Checker),
		deadline: 5 * time.Second,
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RegisterChecker adds a named readiness checker. It panics if a checker with
// the same name is already registered (fail-fast at startup, matching Go
// convention for registration functions like http.HandleFunc). Passing a nil
// fn also panics for the same reason.
//
// The supplied function is wrapped with wrapCtxSafe before being stored, so
// the effective Checker honours ctx.Done regardless of the inner
// implementation's cooperativeness. See wrapCtxSafe for the full contract.
func (h *Handler) RegisterChecker(name string, fn Checker) {
	if fn == nil {
		panic(fmt.Sprintf("health: nil checker for %q", name))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.checkers[name]; exists {
		panic(fmt.Sprintf("health: duplicate checker name %q", name))
	}
	h.checkers[name] = wrapCtxSafe(fn)
}

// SetVerboseToken sets a bearer token that must be provided via the
// X-Readyz-Token header to access /readyz?verbose output. After PR-A35 the
// token gate is no longer optional: requests that carry ?verbose but do not
// match receive 401, and requests that carry ?verbose while no token is
// configured also receive 401. Operators who deliberately do not want the
// verbose endpoint must use WithVerboseDisabled instead of relying on an
// absent token.
//
// ref: Kubernetes withholds error reasons in verbose output but exposes check
// names. GoCell goes further: the entire verbose block (cell names, dependency
// names) is gated behind a token, and the plain /readyz endpoint remains
// reachable without any gate for Kubernetes readiness probes.
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

// readyzResult bundles everything a ReadyzHandler needs to produce a response.
// Captured inside singleflight.Do so concurrent requests share one probe pass.
type readyzResult struct {
	allHealthy   bool
	cells        map[string]string
	dependencies map[string]map[string]any
}

// ReadyzHandler returns an http.HandlerFunc for the /readyz readiness endpoint.
// It runs all registered readiness checkers in parallel, each bounded by
// h.deadline. The probe context is derived from context.Background() (not
// r.Context()) so that kubelet/LB connection drops do not cancel in-flight
// probes.
//
// By default it returns only aggregate readiness status. Detailed cell and
// dependency breakdown is returned only when the request enables verbose mode
// AND carries a matching X-Readyz-Token. Verbose requests without a valid
// token receive 401 — prior behaviour was a silent downgrade to 200.
//
// Concurrent /readyz calls share one probe execution via singleflight; there
// is no fixed concurrency ceiling.
//
// ref: k8s.io/apiserver/pkg/server/healthz — server-side deadline, probe
// independence from request lifecycle.
func (h *Handler) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.shuttingDown.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "shutting_down",
			})
			return
		}
		verbose, denied := h.verboseDecision(r)
		if denied {
			h.sendVerboseDenied(w)
			return
		}

		key := sfKeyAggregate
		if verbose {
			key = sfKeyVerbose
		}
		// computeReadyzSafe wraps aggregateCellHealth/runProbesParallel with
		// a recover fence so a panic in any helper does not propagate to
		// every sharer blocked on singleflight.Do (per-probe panics are
		// already caught by runOneProbe — this layer covers the rarer
		// "assembly helper panic" class).
		shared, _, _ := h.sf.Do(key, func() (any, error) {
			return h.computeReadyzSafe(verbose), nil
		})
		result, ok := shared.(readyzResult)
		if !ok {
			slog.Error("readyz: singleflight returned unexpected payload; failing closed",
				slog.Any("value", shared))
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unhealthy"})
			return
		}
		writeReadyzResponse(w, h, result.allHealthy, verbose, result.cells, result.dependencies)
	}
}

// computeReadyzSafe wraps computeReadyz with a recover fence so that a
// panic in aggregateCellHealth, h.assembly.Health(), or any future helper
// does not propagate out of singleflight.Do — which would otherwise surface
// the panic to every concurrent sharer. On recover we fail closed with a
// plain unhealthy result (no cells / dependencies) and log the event.
// Per-probe panics are caught separately inside runOneProbe.
func (h *Handler) computeReadyzSafe(verbose bool) (result readyzResult) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("readyz: recovered panic during readiness computation",
				slog.Any("panic", r))
			result = readyzResult{allHealthy: false}
		}
	}()
	return h.computeReadyz(verbose)
}

// computeReadyz runs the cell health snapshot + all readiness probes and
// returns a readyzResult. Invoked inside singleflight.Do; constructs a fresh
// result on each invocation. Callers must go through computeReadyzSafe so
// that panics do not escape the singleflight boundary.
func (h *Handler) computeReadyz(verbose bool) readyzResult {
	allHealthy, cells := h.aggregateCellHealth(verbose)

	h.mu.RLock()
	checkersCopy := make(map[string]Checker, len(h.checkers))
	for k, v := range h.checkers {
		checkersCopy[k] = v
	}
	h.mu.RUnlock()

	results := h.runProbesParallel(checkersCopy)
	probeHealthy, dependencies := h.aggregateProbeResults(results, verbose)
	if !probeHealthy {
		allHealthy = false
	}
	return readyzResult{allHealthy: allHealthy, cells: cells, dependencies: dependencies}
}

// aggregateCellHealth computes cell readiness and optionally builds the verbose
// cells map. Returns (allHealthy, cells).
func (h *Handler) aggregateCellHealth(verbose bool) (bool, map[string]string) {
	cellHealth := h.assembly.Health()
	var cells map[string]string
	if verbose {
		cells = make(map[string]string, len(cellHealth))
	}
	allHealthy := true
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

// aggregateProbeResults converts ProbeResult map to verbose dependency output.
// Returns (allHealthy, dependencies). dependencies is nil when verbose is false.
func (h *Handler) aggregateProbeResults(results map[string]ProbeResult, verbose bool) (bool, map[string]map[string]any) {
	var dependencies map[string]map[string]any
	if verbose {
		dependencies = make(map[string]map[string]any, len(results))
	}
	allHealthy := true
	for name, pr := range results {
		if pr.Status != "healthy" {
			allHealthy = false
		}
		if verbose {
			entry := map[string]any{
				"status":      pr.Status,
				"duration_ms": pr.Duration.Milliseconds(),
			}
			if pr.Err != nil {
				entry["error"] = truncateErrMsg(pr.Err.Error(), maxVerboseErrLen)
			}
			dependencies[name] = entry
		}
	}
	return allHealthy, dependencies
}

// writeReadyzResponse serialises and sends the /readyz HTTP response.
func writeReadyzResponse(
	w http.ResponseWriter,
	h *Handler,
	allHealthy, verbose bool,
	cells map[string]string,
	dependencies map[string]map[string]any,
) {
	status := "healthy"
	httpStatus := http.StatusOK
	if !allHealthy {
		status = "unhealthy"
		httpStatus = http.StatusServiceUnavailable
	}

	response := map[string]any{
		"status": status,
	}
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

// runProbesParallel executes all checkers in parallel bounded by h.deadline.
// Because RegisterChecker wraps every fn with wrapCtxSafe, each goroutine
// returns promptly when the aggregate deadline fires — the pre-PR-A35
// "goroutine leak past handler return" trade-off no longer applies at this
// layer.
//
// The probe ctx is derived from context.Background() so that request-level
// cancellation (kubelet disconnect) does not cancel probes.
//
// ref: k8s.io/apiserver/pkg/server/healthz — background-ctx readyz deadline.
func (h *Handler) runProbesParallel(checkers map[string]Checker) map[string]ProbeResult {
	results := make(map[string]ProbeResult, len(checkers))
	if len(checkers) == 0 {
		return results
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.deadline)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(checkers))
	for name, fn := range checkers {
		name, fn := name, fn
		go func() {
			defer wg.Done()
			pr := runOneProbe(ctx, fn)
			mu.Lock()
			results[name] = pr
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

// runOneProbe executes a single Checker inside a recover fence and returns a
// ProbeResult. A panicking probe is caught and reported as unhealthy.
// pr.Duration is written by the defer so it covers both the happy path and
// the panic path in one place.
func runOneProbe(ctx context.Context, fn Checker) (pr ProbeResult) {
	start := time.Now()
	defer func() {
		pr.Duration = time.Since(start)
		if r := recover(); r != nil {
			pr.Status = "unhealthy"
			pr.Err = fmt.Errorf("panic: %v", r)
		}
	}()

	err := fn(ctx)
	if err == nil {
		pr.Status = "healthy"
		return pr
	}
	if isDeadlineError(ctx, err) {
		pr.Status = "timeout"
		pr.Err = fmt.Errorf("probe did not return within deadline (ctx: %w)", err)
		return pr
	}
	pr.Status = "unhealthy"
	pr.Err = err
	return pr
}

// isDeadlineError reports whether the probe ended via context cancellation
// rather than a domain-level failure. It returns true for both
// DeadlineExceeded (aggregator timed the probe out) and Canceled (test
// harnesses, shutdown paths) so operators see a uniform "timeout" status
// instead of Canceled being reported as domain-level unhealthy. Distinct
// semantics are still preserved in pr.Err for diagnostics.
func isDeadlineError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// verboseDecision inspects the request and returns:
//   - (false, false) — request did not ask for verbose → serve plain aggregate
//   - (true, false)  — request asked for verbose and is authorised → serve verbose
//   - (_, true)      — request asked for verbose but is denied → 401 response
//
// PR-A35: denial no longer silently downgrades to 200. This makes
// misconfigured operators (wrong header, forgotten token) visible without
// impacting Kubernetes readinessProbes, which must not pass ?verbose.
func (h *Handler) verboseDecision(r *http.Request) (verbose, denied bool) {
	if !readyzVerbose(r) {
		return false, false
	}
	h.mu.RLock()
	token := h.verboseToken
	disabled := h.verboseDisabled
	h.mu.RUnlock()
	if disabled {
		// Operators who set WithVerboseDisabled have explicitly waived the
		// verbose endpoint; ?verbose gets the plain body. Debug-level log
		// so the path is visible for diagnostics without spamming prod.
		slog.Debug("readyz: verbose requested but endpoint is disabled; serving plain aggregate",
			slog.String("remote_addr", r.RemoteAddr))
		return false, false
	}
	if token == "" {
		slog.Warn("readyz: verbose requested but no token configured; denying",
			slog.String("remote_addr", r.RemoteAddr))
		return false, true
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(VerboseTokenHeader)), []byte(token)) == 1 {
		return true, false
	}
	slog.Warn("readyz: verbose token mismatch; denying",
		slog.String("remote_addr", r.RemoteAddr))
	return false, true
}

// sendVerboseDenied writes the 401 response for a rejected verbose request.
// Body uses the project-standard errcode envelope (code/message/details) so
// machine-side monitoring can distinguish this from other 401s (e.g. JWT
// failures elsewhere). The details map is empty today but the key is
// present so consumers can rely on the shape.
func (h *Handler) sendVerboseDenied(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			"code":    string(errcode.ErrReadyzVerboseDenied),
			"message": "verbose output requires a matching X-Readyz-Token header",
			"details": map[string]any{},
		},
	})
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

// truncateErrMsg limits msg to max runes, appending "..." when truncated.
// Used to bound error strings written into /readyz?verbose output so that
// a single verbose entry cannot carry unbounded diagnostic text.
func truncateErrMsg(msg string, max int) string {
	if len(msg) <= max {
		return msg
	}
	return msg[:max] + "..."
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("health: failed to write response", slog.String("error", err.Error()))
	}
}
