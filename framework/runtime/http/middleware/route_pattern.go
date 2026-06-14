// route_pattern.go shares a request-scoped *patternRecorder between
// runtime/http/router (which writes the matched ServeMux pattern via
// RecordRoutePattern in the dispatch wrapper) and observability middleware
// (which reads it through RouteFor after next.ServeHTTP returns).
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
//
// Short-circuit paths (auth reject, rate limit, circuit breaker, body limit,
// 405) complete before patternRecordingMux runs, so the recorder is still
// empty. The router injects a RouteResolver via WithRouteResolver so RouteFor
// can fall back to it and all three observability layers (Metrics, AccessLog,
// Tracing) see a consistent, non-"unmatched" label on those paths.
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

// RouteResolver maps a concrete (method, urlPath) to a low-cardinality route
// pattern. Routers inject one via WithRouteResolver before the observability
// middleware chain runs so RouteFor can recover the pattern on requests
// rejected before dispatch (auth, rate limit, circuit breaker, body limit,
// 405). Returns ok=false when the request does not match any registered route.
type RouteResolver func(method, urlPath string) (pattern string, ok bool)

type routeResolverKey struct{}

// WithRouteResolver returns a copy of ctx carrying fn as the shared
// router-supplied route resolver. Called once per request by the router root
// before observability middleware runs.
func WithRouteResolver(ctx context.Context, fn RouteResolver) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, routeResolverKey{}, fn)
}

// RouteResolverFrom retrieves the resolver installed by WithRouteResolver,
// or nil when none is registered (e.g. a middleware unit test that builds
// a chain without the router).
func RouteResolverFrom(ctx context.Context) RouteResolver {
	if fn, ok := ctx.Value(routeResolverKey{}).(RouteResolver); ok {
		return fn
	}
	return nil
}

// RouteFor returns the matched route pattern for the current request. It reads
// the dispatch-time recorder first, then falls back to the router-supplied
// RouteResolver (typically populated for short-circuit rejections that returned
// before patternRecordingMux ran). Returns UnmatchedRoute when neither source
// produces a non-empty pattern.
//
// Tracing / AccessLog / Metrics share this single accessor so their route
// labels stay consistent on reject paths.
func RouteFor(ctx context.Context, method, urlPath string) string {
	if rec, ok := ctx.Value(patternRecorderKey{}).(*patternRecorder); ok && rec.pattern != "" {
		return rec.pattern
	}
	if resolver := RouteResolverFrom(ctx); resolver != nil {
		if p, ok := resolver(method, urlPath); ok && p != "" {
			return p
		}
	}
	return UnmatchedRoute
}
