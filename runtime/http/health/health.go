// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints. /readyz returns aggregate readiness by default and only exposes
// detailed cell and dependency breakdown in verbose mode.
//
// ref: k8s.io/apiserver/pkg/server/healthz — readyz deadline + named probes.
// ref: uber-go/fx internal/lifecycle/lifecycle.go — ctx-aware lifecycle hooks.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/runtime/http/health/probequery"
)

// maxVerboseErrLen is the maximum length of a probe error string included in
// /readyz?verbose output. Error strings exceeding this limit are truncated
// with an ellipsis to bound response size and limit accidental exposure of
// long, potentially sensitive diagnostic messages.
const maxVerboseErrLen = 512

// Checker is a named readiness probe. Returning a non-nil error marks the
// check as unhealthy. The context carries the deadline set on the Handler
// (default 5 s, matching Kubernetes readiness probe convention).
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

// Handler exposes /healthz and /readyz endpoints.
//
// Verbose-mode access control is *not* this package's concern. The handler
// emits the verbose body whenever the request opts in via the ?verbose query
// parameter (parsed by probequery.Verbose). Bearer-token gating, IP allow-
// listing, or any other access-control policy must be installed by the
// caller as HTTP middleware ahead of the handler — typically by attaching
// bootstrap.PolicyVerboseToken to the readyz cell.RouteGroup.
type Handler struct {
	assembly *assembly.CoreAssembly

	// deadline is the per-probe timeout for /readyz. Default 5 s mirrors
	// Kubernetes readiness probe convention and is independent of the kubelet
	// HTTP connection deadline so that kubelet connection drops do not cancel
	// in-flight probes.
	deadline time.Duration

	mu           sync.RWMutex
	checkers     map[string]Checker
	adapterInfo  map[string]string // static adapter metadata for verbose output
	shuttingDown atomic.Bool
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
// convention for registration functions like http.HandleFunc).
func (h *Handler) RegisterChecker(name string, fn Checker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.checkers[name]; exists {
		panic(fmt.Sprintf("health: duplicate checker name %q", name))
	}
	h.checkers[name] = fn
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
// It runs all registered readiness checkers in parallel, each bounded by
// h.deadline. The probe context is derived from context.Background() (not
// r.Context()) so that kubelet/LB connection drops do not cancel in-flight
// probes.
//
// By default it returns only aggregate readiness status. Detailed cell and
// dependency breakdown is returned only when the request enables verbose mode.
//
// Security: verbose=true exposes internal topology (cell names, dependency
// names). Gate verbose access at the HTTP middleware layer — typically by
// attaching bootstrap.PolicyVerboseToken to the readyz cell.RouteGroup so
// the policy 401's the request before it reaches this handler.
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
		verbose := probequery.Verbose(r)

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

		writeReadyzResponse(w, h, allHealthy, verbose, cells, dependencies)
	}
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

// runProbesParallel executes all checkers in parallel, bounded by h.deadline
// at the aggregator level. When the deadline fires, probes that have not yet
// returned are marked with Status="timeout" and the function returns
// immediately — their goroutines leak until the underlying probe naturally
// exits. This is an intentional trade-off: bounding /readyz response time
// (operational priority) over bounding goroutine lifetime. Probes MUST
// honour ctx.Done to avoid leaks; probes that ignore ctx will leak.
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

	// Derive a deadline context from Background — independent of the HTTP
	// request context so kubelet/LB disconnects do not cancel probes.
	ctx, cancel := context.WithTimeout(context.Background(), h.deadline)
	defer cancel()

	var mu sync.Mutex

	// Start one goroutine per probe; each writes its ProbeResult into
	// results only if the deadline branch hasn't already filled that slot.
	// NOTE: An uncooperative probe (one that ignores ctx) will leak its
	// goroutine past this function's return — its goroutine continues
	// running until the probe naturally exits. This is the known trade-off
	// of hard deadline + cooperative ctx: we choose to bound /readyz
	// response time (operational priority) over bounding goroutine
	// lifetime. Operators SHOULD make probes honour ctx.Done.
	var wg sync.WaitGroup
	wg.Add(len(checkers))
	for name, fn := range checkers {
		name, fn := name, fn // capture loop vars
		go func() {
			defer wg.Done()
			pr := runOneProbe(ctx, fn)
			mu.Lock()
			if _, filled := results[name]; !filled {
				results[name] = pr
			}
			mu.Unlock()
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		// All probes completed before deadline.
	case <-ctx.Done():
		// Deadline exceeded; fill every unfilled slot with "timeout".
		// Late-arriving probes (see goroutine above) will see their slot
		// already filled and skip the write.
		mu.Lock()
		for name := range checkers {
			if _, ok := results[name]; !ok {
				results[name] = ProbeResult{
					Status:   "timeout",
					Duration: h.deadline,
					Err: fmt.Errorf("probe %q did not return within deadline %s (ctx: %w)",
						name, h.deadline, ctx.Err()),
				}
			}
		}
		mu.Unlock()
	}

	// Snapshot results under lock to insulate caller from late-arriving
	// probe goroutines that may still mutate the map.
	mu.Lock()
	snap := make(map[string]ProbeResult, len(results))
	for k, v := range results {
		snap[k] = v
	}
	mu.Unlock()
	return snap
}

// runOneProbe executes a single Checker inside a recover fence and returns a
// ProbeResult. A panicking probe is caught and reported as unhealthy.
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
	pr.Duration = time.Since(start) // updated again by defer, but set here for clarity
	if err == nil {
		pr.Status = "healthy"
		return pr
	}
	if isDeadlineError(ctx, err) {
		pr.Status = "timeout"
	} else {
		pr.Status = "unhealthy"
	}
	pr.Err = err
	return pr
}

// isDeadlineError reports whether the probe timed out due to the probe ctx
// deadline. It checks both ctx.Err() (context was cancelled/timed-out) and
// whether the returned error wraps context.DeadlineExceeded.
func isDeadlineError(ctx context.Context, err error) bool {
	if ctx.Err() == context.DeadlineExceeded {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
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
