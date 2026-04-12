package middleware

import (
	"context"

	"github.com/go-chi/chi/v5"
)

// UnmatchedRoute is the sentinel route label used when a request does not match
// any registered route (e.g. 404). Using a fixed string prevents random paths
// from creating unbounded metric/span cardinality.
//
// ref: slok/go-http-metrics — explicit handlerID pattern for 404 fallback
const UnmatchedRoute = "unmatched"

// RoutePatternFromCtx extracts the chi route pattern from ctx.
// Must be called AFTER next.ServeHTTP() — chi only populates RoutePattern
// during routing.
//
// Returns UnmatchedRoute when no chi routing context exists or the pattern
// is empty (404 / unmatched requests).
//
// ref: go-chi/chi context.go — RoutePattern() joins RoutePatterns after routing
func RoutePatternFromCtx(ctx context.Context) string {
	rctx := chi.RouteContext(ctx)
	if rctx == nil {
		return UnmatchedRoute
	}
	pattern := rctx.RoutePattern()
	if pattern == "" {
		return UnmatchedRoute
	}
	return pattern
}
