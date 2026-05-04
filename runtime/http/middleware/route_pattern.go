// route_pattern.go shares a request-scoped *patternRecorder between
// runtime/http/router (which writes the matched ServeMux pattern via
// RecordRoutePattern in the dispatch wrapper) and observability middleware
// (which reads it through RoutePatternFromCtx after next.ServeHTTP returns).
//
// The recorder lives in this package — not in router/ — for two reasons:
//  1. Tracing / AccessLog / Metrics already live here and read the value;
//     keeping the accessor next to the readers avoids an import cycle that
//     would otherwise force router → middleware → router.
//  2. The mutable-container-via-context shape is local to the observability
//     contract; router only owns the writer (the dispatch wrapper) and is
//     free to evolve independently.
//
// Recorder writes happen exactly once per request inside the router's
// patternRecordingMux; reads happen many times (one per observing
// middleware) but always after dispatch has returned, so no synchronization
// is required.
package middleware

import "context"

// UnmatchedRoute is the sentinel route label used when a request does not
// match any registered route (e.g. 404). Using a fixed string prevents random
// paths from creating unbounded metric/span cardinality.
//
// ref: slok/go-http-metrics — explicit handlerID pattern for 404 fallback
const UnmatchedRoute = "unmatched"

// patternRecorder is a mutable container shared via request context. The
// router's outermost middleware installs it (WithRoutePatternRecorder); the
// router's innermost dispatch wrapper writes the matched ServeMux pattern
// (RecordRoutePattern) before invoking the leaf handler. All middleware in
// between can read the result after their next.ServeHTTP returns.
type patternRecorder struct {
	pattern string
}

type patternRecorderKey struct{}

// WithRoutePatternRecorder returns a copy of ctx carrying a fresh, empty
// recorder. Called once per request by the router root.
func WithRoutePatternRecorder(ctx context.Context) context.Context {
	return context.WithValue(ctx, patternRecorderKey{}, &patternRecorder{})
}

// RecordRoutePattern stores the matched ServeMux pattern in the recorder
// previously installed by WithRoutePatternRecorder. No-op if no recorder is
// present (e.g. unit tests that exercise a middleware without the router).
func RecordRoutePattern(ctx context.Context, pattern string) {
	if rec, ok := ctx.Value(patternRecorderKey{}).(*patternRecorder); ok {
		rec.pattern = pattern
	}
}

// RoutePatternFromCtx extracts the matched route pattern recorded for the
// current request. Must be called AFTER next.ServeHTTP returns — the recorder
// is populated only once dispatch has selected a registered handler.
//
// Returns UnmatchedRoute when no recorder is installed or the pattern is
// empty (404 / unmatched requests).
func RoutePatternFromCtx(ctx context.Context) string {
	if rec, ok := ctx.Value(patternRecorderKey{}).(*patternRecorder); ok && rec.pattern != "" {
		return rec.pattern
	}
	return UnmatchedRoute
}
