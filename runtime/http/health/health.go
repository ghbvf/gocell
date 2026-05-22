// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints. /readyz returns aggregate readiness by default and only exposes
// detailed cell and dependency breakdown in verbose mode.
//
// PR-A35 made two structural guarantees — now fulfilled by the injected
// [kernel/healthz.Aggregator] (runtime/observability/healthz.NewAggregator):
//   - Each probe is wrapped with a ctx-safe racing wrapper so the outer call
//     returns as soon as the aggregate deadline fires, regardless of whether
//     the inner function cooperates. This removes the "uncooperative probe
//     leaks a goroutine past ReadyzHandler's return" trade-off.
//   - /readyz requests are deduplicated via singleflight so that a burst of
//     concurrent probes shares one probe execution. This replaces the prior
//     plan of a fixed "max concurrent probes" semaphore.
//
// Handler is pure HTTP transport: probe storage and execution are delegated
// to the injected Aggregator. Cell-level health (assembly.Health()) is
// separate from aggregator probes and is not routed through the Aggregator.
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
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/logutil"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/runtime/http/health/probequery"
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

// Option configures a Handler.
type Option func(*Handler)

// WithDeadline is retained for API continuity but is a no-op on Handler
// since probe execution deadlines are now owned by the injected Aggregator.
// Configure probe deadlines via runtime/observability/healthz.WithDeadline at
// Aggregator construction time.
func WithDeadline(_ time.Duration) Option {
	return func(_ *Handler) {}
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

// VerboseAuthHeader is the HTTP header used to authenticate /readyz?verbose
// requests. Verbose access always requires both a matching header and a
// pre-configured token (see SetVerboseToken); PR-A35 removed the prior
// "unconfigured = unrestricted" fallback.
const VerboseAuthHeader = "X-Readyz-Token"

// Handler exposes /healthz and /readyz endpoints. It is pure HTTP transport:
// probe storage and execution are delegated to the injected Aggregator.
// Cell-level health aggregation via assembly.Health() is separate.
type Handler struct {
	assembly *assembly.CoreAssembly
	agg      healthz.Aggregator // injected aggregator; owns probe storage + execution

	// sf deduplicates concurrent /readyz executions so that a burst of
	// probes (e.g. kubelet + load balancer + manual curl) shares one probe
	// pass. This replaces a fixed semaphore: callers never see 503 "too many
	// probes" and there is no magic-number concurrency bound to tune.
	sf singleflight.Group

	mu              sync.RWMutex
	adapterInfo     map[string]string // static adapter metadata for verbose output
	verboseToken    string            // required match for the X-Readyz-Token header; empty means verbose is denied
	verboseDisabled bool              // if true, /readyz?verbose is answered with the plain aggregate body
	shuttingDown    atomic.Bool
	clock           clock.Clock
}

// New creates a Handler backed by the given CoreAssembly and Aggregator.
// The clock is required and panics fast via clock.MustHaveClock.
// asm and agg non-nil-ness is guaranteed by the bootstrap phase0 contract
// (WithHealthAggregator typed-nil guard + WithAssembly required) and by
// test fakes in the unit path; bare-nil ones produce a nil-deref on first
// access, which the standard panic-recovery middleware translates to 500.
//
// Production wiring threads bootstrap.b.clock here;
// tests pass clockmock.New(...) for deterministic deadline checks.
func New(asm *assembly.CoreAssembly, agg healthz.Aggregator, clk clock.Clock, opts ...Option) *Handler {
	clock.MustHaveClock(clk, "health.New")
	h := &Handler{
		assembly: asm,
		agg:      agg,
		clock:    clk,
	}
	for _, o := range opts {
		o(h)
	}
	return h
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
//
// dependencies and slogDependencies are intentionally separated views per
// ADR 202605171200 four-channel model:
//   - dependencies = wire view (channel a body fragment in 200 verbose, OR
//     stripped to nil in 503 per K#08); typed verboseDependencyEntry has
//     no error text by construction
//   - slogDependencies = channel d view (server-side slog only); typed
//     SlogDependencyEntry carries the full redacted error via the
//     newRedactedErrorMsg funnel
type readyzResult struct {
	overall          string // "healthy" | "degraded" | "unhealthy"
	verbose          bool
	cells            map[string]string                 // nil when !verbose
	dependencies     map[string]verboseDependencyEntry // nil when !verbose (wire view)
	slogDependencies map[string]SlogDependencyEntry    // nil when !verbose (channel d)
	adapters         map[string]string                 // nil when !verbose or no adapter info registered
	reason           string                            // optional low-cardinality reason for unhealthy 503 details
}

// ReadyzHandler returns an http.HandlerFunc for the /readyz readiness endpoint.
// It runs all registered readiness probes (via the injected Aggregator) in
// parallel, each bounded by the Aggregator's configured deadline. The probe
// context is derived from context.Background() (not r.Context()) so that
// kubelet/LB connection drops do not cancel in-flight probes.
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
		ctx := r.Context()
		if h.shuttingDown.Load() {
			slog.Info("readyz: shutting down (graceful_shutdown)",
				slog.String("status", readyzStatusShuttingDown),
				slog.String("reason", readyzReasonGracefulShutdown))
			writeReadyz503(ctx, w, readyzStatusShuttingDown, readyzReasonGracefulShutdown)
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
		// computeReadyzSafe wraps aggregateCellHealth/agg.Evaluate with
		// a recover fence so a panic in any helper does not propagate to
		// every sharer blocked on singleflight.Do (per-probe panics are
		// already caught by the Aggregator — this layer covers the rarer
		// "assembly helper panic" class).
		shared, _, _ := h.sf.Do(key, func() (any, error) {
			return h.computeReadyzSafe(verbose), nil
		})
		result, ok := shared.(readyzResult)
		if !ok {
			slog.Error("readyz: singleflight returned unexpected payload; failing closed",
				slog.String("internal_reason", "readiness_computation_failed"),
				slog.Any("value", shared))
			writeReadyz503(ctx, w, "unhealthy", readyzReasonReadinessFailed)
			return
		}
		result.writeTo(ctx, w)
	}
}

// computeReadyzSafe wraps computeReadyz with a recover fence so that a
// panic in aggregateCellHealth, h.assembly.Health(), or any future helper
// does not propagate out of singleflight.Do — which would otherwise surface
// the panic to every concurrent sharer. On recover we fail closed with a
// plain unhealthy result (no cells / dependencies) and log the event.
// Per-probe panics are caught separately inside the Aggregator.
func (h *Handler) computeReadyzSafe(verbose bool) (result readyzResult) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("readyz: recovered panic during readiness computation",
				slog.String("internal_reason", "readiness_computation_failed"),
				slog.Any("panic", redaction.RedactAny(r)))
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
// adapter info is captured under Handler.mu so the result is fully
// self-contained — writeTo can serialize it without touching Handler state.
func (h *Handler) computeReadyz(verbose bool) readyzResult {
	cellOverall, cells := h.aggregateCellHealth(verbose)

	h.mu.RLock()
	var adapters map[string]string
	if verbose {
		adapters = cloneAdapterInfo(h.adapterInfo)
	}
	h.mu.RUnlock()

	// Evaluate uses context.Background() internally so kubelet disconnects
	// do not cancel in-flight probes (PR-A35 / Aggregator contract).
	snap := h.agg.Evaluate(context.Background())
	agg := h.aggregateProbeResults(snap.Probes, verbose)
	worst := rankStatus(cellOverall)
	if r := rankStatus(agg.Overall); r > worst {
		worst = r
	}
	return readyzResult{
		overall:          statusFromRank(worst),
		verbose:          verbose,
		cells:            cells,
		dependencies:     agg.Wire,
		slogDependencies: agg.SlogDiag,
		adapters:         adapters,
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

// probeAggregate is the typed return value of aggregateProbeResults. It
// bundles the worst-case overall status together with the two parallel views
// of the probe outcome (wire shape and slog ops-diagnostics shape) into a
// single struct so callers do not depend on positional tuple ordering.
type probeAggregate struct {
	// Overall is the worst-case status across all probe results:
	// "healthy" | "degraded" | "unhealthy".
	Overall string
	// Wire is the public HTTP body fragment (channel a); typed
	// verboseDependencyEntry is frozen to {status, duration_ms}, no error text.
	// nil when verbose is false.
	Wire map[string]verboseDependencyEntry
	// SlogDiag is the ops-diagnostics slog payload (channel d); typed
	// SlogDependencyEntry carries the redacted error via newRedactedErrorMsg.
	// nil when verbose is false.
	SlogDiag map[string]SlogDependencyEntry
}

// aggregateProbeResults converts a kernel healthz.Snapshot.Probes slice into a
// probeAggregate per ADR 202605171200 four-channel model:
//
//   - Wire: map[name]verboseDependencyEntry — public payload, frozen to
//     {Status, DurationMs}; no error text by construction.
//   - SlogDiag: map[name]SlogDependencyEntry — ops-diagnostics (channel d);
//     ErrorMsg is typed redactedErrorMsg, produced only via the
//     newRedactedErrorMsg funnel which routes through pkg/redaction.RedactString.
//
// Both views are nil when verbose is false. Overall is the worst-case status
// across all probe results: healthy(0) < degraded(1) < unhealthy(2).
//
// Wire-string mapping from kernel Status enum:
//   - StatusUp       → "healthy"
//   - StatusDegraded → "degraded"
//   - StatusDown     → "timeout"   if errors.Is(Err, context.DeadlineExceeded)
//   - StatusDown     → "unhealthy" otherwise
//
// ref: k8s.io/apiserver/pkg/server/healthz healthz.go:274-275 — wire vs klog
// double-buffer separation; GoCell uses redaction in place of K8s "reason
// withheld" but preserves the same wire-no-text invariant.
func (h *Handler) aggregateProbeResults(
	results []healthz.ProbeResult, verbose bool,
) probeAggregate {
	var wire map[string]verboseDependencyEntry
	var slogDiag map[string]SlogDependencyEntry
	if verbose {
		wire = make(map[string]verboseDependencyEntry, len(results))
		slogDiag = make(map[string]SlogDependencyEntry, len(results))
	}
	worst := 0 // healthy
	for _, pr := range results {
		wireStatus := kernelStatusToWire(pr.Status, pr.Err)
		if r := rankStatus(wireStatus); r > worst {
			worst = r
		}
		if verbose {
			wire[pr.Name] = verboseDependencyEntry{
				Status:     wireStatus,
				DurationMs: pr.Latency.Milliseconds(),
			}
			slogDiag[pr.Name] = SlogDependencyEntry{
				status:     wireStatus,
				durationMs: pr.Latency.Milliseconds(),
				errorMsg:   newRedactedErrorMsg(pr.Err),
			}
		}
	}
	return probeAggregate{
		Overall:  statusFromRank(worst),
		Wire:     wire,
		SlogDiag: slogDiag,
	}
}

// kernelStatusToWire maps a kernel healthz.Status enum value to the wire
// string used in HTTP responses and slog channel d:
//   - StatusUp       → "healthy"
//   - StatusDegraded → "degraded"
//   - StatusDown     → "timeout"   if errors.Is(err, context.DeadlineExceeded)
//   - StatusDown     → "unhealthy" otherwise
//
// The "timeout" distinction preserves the existing wire contract: dashboards
// can distinguish "probe overran deadline" from "probe returned a domain error"
// without the HTTP transport needing to know about kernel-layer deadline
// semantics beyond the errors.Is check.
func kernelStatusToWire(s healthz.Status, err error) string {
	switch s {
	case healthz.StatusUp:
		return "healthy"
	case healthz.StatusDegraded:
		return "degraded"
	default: // StatusDown
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		return "unhealthy"
	}
}

// writeTo serializes the readyz result.
//
//	200 → {"data": {"status":"healthy"|"degraded", ...verbose fields}}
//	      verbose fields use typed verboseDependencyEntry — wire shape is
//	      {status, duration_ms}, no error text per ADR 202605171200 §3
//	503 → canonical errcode envelope via httputil.WriteError; details is
//	      the empty array per K#08 5xx redaction policy. Verbose breakdown
//	      (cells/typed slogDependencies/adapters) and the readiness reason
//	      are emitted to server-side slog (channel d) so operators can
//	      diagnose without relying on the public wire body.
//
// degraded maps to HTTP 200 — a degraded service (fail-open) should NOT
// trigger pod eviction; operators monitor degraded via the response body
// status field or the underlying Prometheus counter.
// ref: envoyproxy/envoy admin /ready — DEGRADED returns 200.
// ref: k8s.io/apiserver/pkg/server/healthz healthz.go:274-275 — wire/klog
// double-buffer separation; full error text never leaves the server.
func (r readyzResult) writeTo(ctx context.Context, w http.ResponseWriter) {
	body := r.verboseFields()
	body["status"] = r.overall
	switch r.overall {
	case "healthy":
		writeJSON(w, http.StatusOK, envelopeData(body))
	case "degraded":
		// HTTP 200 — degraded does NOT trigger pod eviction (fail-open semantic).
		// ref: envoyproxy/envoy admin /ready — DEGRADED returns 200.
		// Emit channel d ops-diagnostics at Info level so operators can observe
		// degraded dependency ErrorMsg without triggering a warn-level alert.
		r.logDiagnostics(slog.LevelInfo, "readyz degraded")
		writeJSON(w, http.StatusOK, envelopeData(body))
	default: // "unhealthy"
		reason := r.reason
		if reason == "" {
			reason = readyzReasonReadinessFailed
		}
		r.logDiagnostics(slog.LevelWarn, "readyz unhealthy", slog.String("reason", reason))
		writeReadyz503(ctx, w, r.overall, reason)
	}
}

// logDiagnostics emits the channel d (ops-diagnostics) breakdown to slog so
// operators retain the diagnostic data per ADR 202605171200 §3. Public
// responses always carry no error text on wire (D1 decision).
//
// The slogDependencies map (not the wire dependencies) is what gets logged
// — its typed SlogDependencyEntry.ErrorMsg field carries the redacted error
// text via the newRedactedErrorMsg funnel (HEALTH-REDACTED-ERROR-MSG-FUNNEL-01),
// while the wire dependencies map carries only {status, duration_ms} per
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01.
//
// level controls the slog emit level:
//   - slog.LevelWarn for "unhealthy" (downstream hard failure)
//   - slog.LevelInfo for "degraded"  (fail-open, operators monitor but no alert)
//
// extra carries caller-specific attrs (e.g. "reason" for the unhealthy path).
// degraded callers pass no extra attrs.
//
// This covers both degraded (HTTP 200) and unhealthy (HTTP 503) paths, ensuring
// slog channel d captures probe ErrorMsg regardless of the HTTP status code.
// Previously only the unhealthy path called this function, so degraded probe
// ErrorMsg was silently dropped (F3 / PR #552 review finding).
//
// Cells/dependencies/adapters maps are appended only on verbose probes so
// that high-frequency k8s readiness probes (typically every 5s) don't spam
// log backends with full breakdown when only status/reason are actionable.
func (r readyzResult) logDiagnostics(level slog.Level, msg string, extra ...slog.Attr) {
	attrs := []any{
		slog.String("status", r.overall),
	}
	for _, a := range extra {
		attrs = append(attrs, a)
	}
	if r.verbose {
		// `dependencies` is emitted as slog.Group rather than slog.Any(map):
		// inside a Group, each SlogDependencyEntry passes through slog.Any
		// where the handler calls Value.Resolve() → entry.LogValue() →
		// GroupValue with snake_case fields. This is the ONLY shape that
		// gives consistent snake_case output across JSON / text / logfmt
		// handlers; slog.Any(map) is opaque-blob and bypasses LogValue.
		// See verbose_shape.go godoc and ADR 202605171200 §2 D6.
		depAttrs := make([]any, 0, len(r.slogDependencies))
		for name, entry := range r.slogDependencies {
			depAttrs = append(depAttrs, slog.Any(name, entry))
		}
		attrs = append(attrs,
			slog.Any("cells", r.cells),
			slog.Group("dependencies", depAttrs...),
			slog.Any("adapters", r.adapters),
		)
	}
	slog.Log(context.Background(), level, msg, attrs...)
}

// writeReadyz503 emits the canonical errcode 503 envelope shared with all
// other framework error responses. status/reason ride along on WithInternal
// for server-side logs only — the wire body is a 5xx-stripped errcode where
// details is the empty array.
//
// ctx 是 request 上下文（非 probe ctx），用于 wire `requestId` 透传到 errcode 响应
// envelope（httputil.WriteError → ctxkeys.RequestIDFrom；slog 日志键仍是 `request_id`）。
func writeReadyz503(ctx context.Context, w http.ResponseWriter, status, reason string) {
	httputil.WriteError(ctx, w, errcode.New(
		errcode.KindUnavailable,
		errcode.ErrServiceUnavailable,
		readyzPublic503Message,
		errcode.WithInternal(fmt.Sprintf("readyz status=%s reason=%s", status, reason)),
	))
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
	if !probequery.Verbose(r) {
		return false, false
	}
	h.mu.RLock()
	disabled := h.verboseDisabled
	token := h.verboseToken
	h.mu.RUnlock()
	remoteAddr := logutil.SafeAddr(r.RemoteAddr)
	if disabled {
		slog.Debug("readyz: verbose requested but endpoint is disabled; serving plain aggregate",
			slog.String("remote_addr", remoteAddr))
		return false, false
	}
	// SEC-FAIL-CLOSED: no token configured → deny. Operators must explicitly
	// configure a token or disable the verbose endpoint. Silently rendering
	// verbose output when token="" leaks internal health details.
	if token == "" {
		slog.Warn("readyz: verbose requested but no token configured; denying",
			slog.String("reason", "token_unconfigured"),
			slog.String("hint", "set GOCELL_READYZ_VERBOSE_TOKEN or GOCELL_READYZ_VERBOSE_DISABLED=1"),
			slog.String("remote_addr", remoteAddr))
		return false, true
	}
	submitted := sha256.Sum256([]byte(r.Header.Get(VerboseAuthHeader)))
	configured := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(submitted[:], configured[:]) != 1 {
		slog.Warn("readyz: verbose token mismatch at handler layer; denying",
			slog.String("reason", "token_mismatch"),
			slog.String("remote_addr", remoteAddr))
		return false, true
	}
	return true, false
}

// sendVerboseDenied writes the 401 response for a rejected verbose request.
// Uses httputil.WritePublic so the response carries wire `requestId` (when
// set by middleware) in the canonical envelope shape shared with business
// 4xx responses. Machine-side monitoring can therefore correlate denied
// verbose probes with other request-level signals via the same field. The
// matching slog log key remains `request_id` (snake_case).
func (h *Handler) sendVerboseDenied(w http.ResponseWriter, r *http.Request) {
	httputil.WritePublic(
		r.Context(),
		w,
		errcode.KindUnauthenticated,
		errcode.ErrReadyzVerboseDenied,
		"verbose output requires a matching X-Readyz-Token header",
	)
}

// envelopeData wraps a success payload in the project-standard
// `{"data": ...}` envelope (see .claude/rules/gocell/api-versioning.md).
// Infrastructure endpoints (/healthz, /readyz) align with the same shape as
// business /api/v1/* responses so consumers can parse both uniformly.
func envelopeData(payload map[string]any) map[string]any {
	return map[string]any{"data": payload}
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
