// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints. /readyz returns aggregate readiness by default and only exposes
// detailed cell and dependency breakdown in verbose mode.
//
// PR-A35 made two structural guarantees:
//   - RegisterChecker wraps every checker with wrapCtxSafe so that the outer
//     Checker always returns as soon as ctx is canceled, regardless of whether
//     the inner function cooperates. This removes the "uncooperative probe
//     leaks a goroutine past ReadyzHandler's return" trade-off that previously
//     sat at the aggregator level — the aggregator itself is now insulated
//     from inner-fn behavior.
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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// maxVerboseErrLen is the maximum length of a probe error string included in
// /readyz?verbose output. Error strings exceeding this limit are truncated
// with an ellipsis to bound response size and limit accidental exposure of
// long, potentially sensitive diagnostic messages.
const maxVerboseErrLen = 512

const (
	// defaultHealthDeadline is the default readiness probe execution deadline.
	// Matches the Kubernetes readiness probe convention (5 s).
	defaultHealthDeadline = 5 * time.Second
)

// singleflight keys for the two response shapes. Verbose and non-verbose
// results are not interchangeable (different body fields), so each shape gets
// its own key; concurrent requests of the same shape share one probe pass.
const (
	sfKeyAggregate = "readyz:aggregate"
	sfKeyVerbose   = "readyz:verbose"
)

const (
	readyzPublic503Message       = "service unavailable"
	readyzStatusShuttingDown     = "shutting_down"
	readyzReasonReadinessFailed  = "readiness_failed"
	readyzReasonGracefulShutdown = "graceful_shutdown"
)

// Checker is a named readiness probe. Returning a non-nil error marks the
// check as unhealthy. The context carries the deadline set on the Handler
// (default 5 s, matching Kubernetes readiness probe convention).
//
// RegisterChecker wraps supplied functions with wrapCtxSafe, so the effective
// Checker stored inside Handler honors ctx.Done regardless of the inner
// implementation's cooperativeness. See wrapCtxSafe for the full contract.
//
// ref: k8s.io/apiserver/pkg/server/healthz — HealthChecker interface with ctx.
type Checker = func(context.Context) error

// ProbeResult captures the outcome of a single readiness probe execution.
type ProbeResult struct {
	Status   string        // "healthy" | "degraded" | "unhealthy" | "timeout"
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
		deadline: defaultHealthDeadline,
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RegisterChecker adds a named readiness checker. It returns an error when a
// checker with the same name is already registered or when fn is nil.
//
// The supplied function is wrapped with wrapCtxSafe before being stored, so
// the effective Checker honors ctx.Done regardless of the inner
// implementation's cooperativeness. See wrapCtxSafe for the full contract.
func (h *Handler) RegisterChecker(name string, fn Checker) error {
	if fn == nil {
		return fmt.Errorf("health: nil checker for %q", name)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.checkers[name]; exists {
		return fmt.Errorf("health: duplicate checker name %q", name)
	}
	h.checkers[name] = wrapCtxSafe(fn)
	return nil
}

// MustRegisterChecker is the static-wiring variant of RegisterChecker.
func (h *Handler) MustRegisterChecker(name string, fn Checker) {
	if err := h.RegisterChecker(name, fn); err != nil {
		panic(err.Error())
	}
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
	h.adapterInfo = cloneAdapterInfo(info)
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
// is alive. Readiness details belong to /readyz. The body uses the
// project-standard {"data": {...}} envelope so machine consumers treat
// infrastructure and business responses uniformly (PR-A35 alignment).
func (h *Handler) LivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, envelopeData(map[string]any{
			"status": "healthy",
		}))
	}
}

// readyzResult bundles everything a /readyz response needs. Computed once
// per singleflight pass and shared by every concurrent request that joined
// the same key. The struct owns its data — adapter info is snapshotted
// under Handler.mu inside computeReadyz so writeTo runs lock-free.
type readyzResult struct {
	overall      string // "healthy" | "degraded" | "unhealthy"
	verbose      bool
	cells        map[string]string         // nil when !verbose
	dependencies map[string]map[string]any // nil when !verbose
	adapters     map[string]string         // nil when !verbose or no adapter info registered
	reason       string                    // optional low-cardinality reason for unhealthy 503 details
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
// token receive 401 — prior behavior was a silent downgrade to 200.
//
// Concurrent /readyz calls share one probe execution via singleflight; there
// is no fixed concurrency ceiling.
//
// ref: k8s.io/apiserver/pkg/server/healthz — server-side deadline, probe
// independence from request lifecycle.
func (h *Handler) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.shuttingDown.Load() {
			writeJSON(w, http.StatusServiceUnavailable, envelopeError(
				errcode.PublicCodeForStatus(http.StatusServiceUnavailable),
				readyzPublic503Message,
				readyzDetails(readyzStatusShuttingDown, readyzReasonGracefulShutdown, nil),
			))
			return
		}
		verbose, denied := h.verboseDecision(r)
		if denied {
			h.sendVerboseDenied(w, r)
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
				slog.String("internal_reason", "readiness_computation_failed"),
				slog.Any("value", shared))
			writeJSON(w, http.StatusServiceUnavailable, envelopeError(
				errcode.PublicCodeForStatus(http.StatusServiceUnavailable),
				readyzPublic503Message,
				readyzDetails("unhealthy", readyzReasonReadinessFailed, nil),
			))
			return
		}
		result.writeTo(w)
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
				slog.String("internal_reason", "readiness_computation_failed"),
				slog.Any("panic", r))
			result = readyzResult{overall: "unhealthy", reason: readyzReasonReadinessFailed}
		}
	}()
	return h.computeReadyz(verbose)
}

// computeReadyz runs the cell health snapshot + all readiness probes and
// returns a readyzResult. Invoked inside singleflight.Do; constructs a fresh
// result on each invocation. Callers must go through computeReadyzSafe so
// that panics do not escape the singleflight boundary.
//
// adapter info is captured under Handler.mu alongside the checkers map so
// the result is fully self-contained — writeTo can serialize it without
// touching Handler state.
func (h *Handler) computeReadyz(verbose bool) readyzResult {
	cellOverall, cells := h.aggregateCellHealth(verbose)

	h.mu.RLock()
	checkersCopy := make(map[string]Checker, len(h.checkers))
	maps.Copy(checkersCopy, h.checkers)
	var adapters map[string]string
	if verbose {
		adapters = cloneAdapterInfo(h.adapterInfo)
	}
	h.mu.RUnlock()

	results := h.runProbesParallel(checkersCopy)
	probeOverall, dependencies := h.aggregateProbeResults(results, verbose)
	worst := rankStatus(cellOverall)
	if r := rankStatus(probeOverall); r > worst {
		worst = r
	}
	return readyzResult{
		overall:      statusFromRank(worst),
		verbose:      verbose,
		cells:        cells,
		dependencies: dependencies,
		adapters:     adapters,
	}
}

func cloneAdapterInfo(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

// aggregateCellHealth computes cell readiness and optionally builds the verbose
// cells map. Returns (overall, cells) where overall is the worst-case status
// across all cells: healthy(0) < degraded(1) < unhealthy(2).
func (h *Handler) aggregateCellHealth(verbose bool) (string, map[string]string) {
	cellHealth := h.assembly.Health()
	var cells map[string]string
	if verbose {
		cells = make(map[string]string, len(cellHealth))
	}
	worst := 0 // healthy
	for id, hs := range cellHealth {
		if verbose {
			cells[id] = hs.Status
		}
		if r := rankStatus(hs.Status); r > worst {
			worst = r
		}
	}
	return statusFromRank(worst), cells
}

// aggregateProbeResults converts ProbeResult map to verbose dependency output.
// Returns (overall, dependencies) where overall is the worst-case status
// across all probe results: healthy(0) < degraded(1) < unhealthy(2).
// dependencies is nil when verbose is false.
func (h *Handler) aggregateProbeResults(results map[string]ProbeResult, verbose bool) (string, map[string]map[string]any) {
	var dependencies map[string]map[string]any
	if verbose {
		dependencies = make(map[string]map[string]any, len(results))
	}
	worst := 0 // healthy
	for name, pr := range results {
		if r := rankStatus(pr.Status); r > worst {
			worst = r
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
	return statusFromRank(worst), dependencies
}

// writeTo serializes the readyz result through the project-standard envelope:
//
//	200 → {"data":  {"status":"healthy"|"degraded", ...verbose fields}}
//	503 → {"error": {"code":"ERR_SERVICE_UNAVAILABLE", "message":"service unavailable",
//	                  "details": {"status":"unhealthy","reason":"readiness_failed", ...verbose fields}}}
//
// degraded maps to HTTP 200 — a degraded service (fail-open) should NOT
// trigger pod eviction; operators monitor degraded via the response body
// status field or the underlying Prometheus counter.
// ref: envoyproxy/envoy admin /ready — DEGRADED returns 200.
//
// Verbose fields (cells, dependencies, adapters) live under data or details
// so consumers walk one consistent path regardless of status. Adapters are
// omitted when no adapter info has been registered.
func (r readyzResult) writeTo(w http.ResponseWriter) {
	body := r.verboseFields()
	body["status"] = r.overall
	switch r.overall {
	case "healthy", "degraded":
		// HTTP 200 — degraded does NOT trigger pod eviction.
		writeJSON(w, http.StatusOK, envelopeData(body))
	default: // "unhealthy"
		reason := r.reason
		if reason == "" {
			reason = readyzReasonReadinessFailed
		}
		body["reason"] = reason
		writeJSON(w, http.StatusServiceUnavailable, envelopeError(
			errcode.PublicCodeForStatus(http.StatusServiceUnavailable),
			readyzPublic503Message,
			body,
		))
	}
}

func readyzDetails(status, reason string, fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["status"] = status
	fields["reason"] = reason
	return fields
}

// verboseFields returns the cells / dependencies / adapters payload (or an
// empty map when the request was non-verbose). Caller adds the per-status
// fields (e.g. "status":"healthy" for the 200 path).
func (r readyzResult) verboseFields() map[string]any {
	body := map[string]any{}
	if !r.verbose {
		return body
	}
	body["cells"] = r.cells
	body["dependencies"] = r.dependencies
	if r.adapters != nil {
		body["adapters"] = r.adapters
	}
	return body
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
		go func() {
			defer wg.Done()
			pr := runOneProbe(ctx, fn, h.deadline)
			mu.Lock()
			results[name] = pr
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

// runOneProbe executes a single Checker inside a recover fence and returns a
// ProbeResult. A panicking probe is caught and reported as unhealthy. The
// deadline value is included verbatim in the timeout error string so verbose
// 503 consumers see the exact budget without having to consult runtime
// configuration.
//
// Timeout vs unhealthy classification matches DeadlineExceeded explicitly
// rather than "any non-nil ctx.Err()". The probe ctx is derived from
// context.WithTimeout(Background, deadline), so the only ctx.Err() value
// that can arise here is DeadlineExceeded — but the explicit match guards
// against a future ctx-parent change silently routing Canceled into the
// timeout bucket. context.Canceled wrapped by client libraries (pgx /
// go-redis on failed I/O) is a genuine unhealthy signal, not a timeout.
func runOneProbe(ctx context.Context, fn Checker, deadline time.Duration) (pr ProbeResult) {
	start := time.Now()
	defer func() {
		pr.Duration = time.Since(start)
		if r := recover(); r != nil {
			pr.Status = "unhealthy"
			pr.Err = fmt.Errorf("panic: %v", r)
		}
	}()

	err := fn(ctx)
	switch {
	case err == nil:
		pr.Status = "healthy"
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		pr.Status = "timeout"
		pr.Err = fmt.Errorf("probe did not return within deadline %s (ctx: %w)", deadline, err)
	case errors.Is(err, cell.ErrDegraded):
		pr.Status = "degraded"
		pr.Err = err
	default:
		pr.Status = "unhealthy"
		pr.Err = err
	}
	return pr
}

// verboseDecision determines whether the request renders the verbose body.
// SEC-FAIL-CLOSED (PR-MODE-1): the default is now fail-closed. When no token
// is configured and verbose is not explicitly disabled, the handler denies the
// verbose request with a 401 (denied=true). Operators must make an explicit
// choice: configure a token (SetVerboseToken / WithReadyzVerboseToken) or
// disable the endpoint (SetVerboseDisabled / WithReadyzVerboseDisabled).
//
// Outcomes:
//
//	(verbose=false, denied=false) — non-verbose query, or WithVerboseDisabled set
//	(verbose=true,  denied=false) — verbose body rendered (token configured + matched)
//	(false,         denied=true)  — no token configured, or token mismatch → 401
//
// See docs/ops/readyz.md for the full state table.
func (h *Handler) verboseDecision(r *http.Request) (verbose, denied bool) {
	if !readyzVerbose(r) {
		return false, false
	}
	h.mu.RLock()
	disabled := h.verboseDisabled
	token := h.verboseToken
	h.mu.RUnlock()
	if disabled {
		slog.Debug("readyz: verbose requested but endpoint is disabled; serving plain aggregate",
			slog.String("remote_addr", r.RemoteAddr))
		return false, false
	}
	// SEC-FAIL-CLOSED: no token configured → deny. Operators must explicitly
	// configure a token or disable the verbose endpoint. Silently rendering
	// verbose output when token="" leaks internal health details.
	if token == "" {
		slog.Warn("readyz: verbose requested but no token configured; denying",
			slog.String("reason", "token_unconfigured"),
			slog.String("hint", "set GOCELL_READYZ_VERBOSE_TOKEN or GOCELL_READYZ_VERBOSE_DISABLED=1"),
			slog.String("remote_addr", r.RemoteAddr))
		return false, true
	}
	submitted := sha256.Sum256([]byte(r.Header.Get(VerboseTokenHeader)))
	configured := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(submitted[:], configured[:]) != 1 {
		slog.Warn("readyz: verbose token mismatch at handler layer; denying",
			slog.String("reason", "token_mismatch"),
			slog.String("remote_addr", r.RemoteAddr))
		return false, true
	}
	return true, false
}

// sendVerboseDenied writes the 401 response for a rejected verbose request.
// Uses httputil.WritePublicError so the response carries request_id (when
// set by middleware) in the canonical envelope shape shared with business
// 4xx responses. Machine-side monitoring can therefore correlate denied
// verbose probes with other request-level signals via the same field.
func (h *Handler) sendVerboseDenied(w http.ResponseWriter, r *http.Request) {
	httputil.WritePublicError(
		r.Context(),
		w,
		http.StatusUnauthorized,
		string(errcode.ErrReadyzVerboseDenied),
		"verbose output requires a matching X-Readyz-Token header",
	)
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
	if msg == "" {
		return ""
	}
	if max <= 0 {
		return "..."
	}
	runes := 0
	for idx := range msg {
		if runes == max {
			return msg[:idx] + "..."
		}
		runes++
	}
	return msg
}

// envelopeData wraps a success payload in the project-standard
// `{"data": ...}` envelope (see .claude/rules/gocell/api-versioning.md).
// Infrastructure endpoints (/healthz, /readyz) align with the same shape as
// business /api/v1/* responses so consumers can parse both uniformly.
func envelopeData(payload map[string]any) map[string]any {
	return map[string]any{"data": payload}
}

// envelopeError wraps an error in the project-standard
// `{"error": {"code":..., "message":..., "details":...}}` envelope (see
// .claude/rules/gocell/error-handling.md). details is normalised to an
// empty map when nil so consumers can always walk the nested path.
func envelopeError(code errcode.Code, message string, details map[string]any) map[string]any {
	if details == nil {
		details = map[string]any{}
	}
	return map[string]any{
		"error": map[string]any{
			"code":    string(code),
			"message": message,
			"details": details,
		},
	}
}

// rankStatus encodes severity ordering for aggregation:
// healthy(0) < degraded(1) < unhealthy(2). timeout maps to unhealthy(2).
// Used by aggregators: result = max(per-source ranks).
func rankStatus(s string) int {
	switch s {
	case "healthy":
		return 0
	case "degraded":
		return 1
	default: // unhealthy, timeout, anything else
		return 2
	}
}

// statusFromRank converts a rank value back to a canonical status string.
func statusFromRank(r int) string {
	switch r {
	case 0:
		return "healthy"
	case 1:
		return "degraded"
	default:
		return "unhealthy"
	}
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("health: failed to write response", slog.Any("error", err))
	}
}
