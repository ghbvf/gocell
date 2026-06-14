package middleware

import (
	"net/http"
	"strings"
)

// buildTestServer mirrors runtime/http/router.Router.Handler so middleware
// tests can exercise route-pattern propagation without pulling in the router
// package (and therefore avoid an import cycle). The composition is:
//
//	WithRoutePatternRecorder → mws (in order) → dispatchWithPatternRecording → mux
//
// Tests register handlers on mux using stdlib 1.22+ patterns
// ("METHOD /path/{param}"); dispatchWithPatternRecording fills the recorder
// so middleware reading RoutePatternFromCtx after next.ServeHTTP returns
// observes the matched pattern.
func buildTestServer(mws []func(http.Handler) http.Handler, register func(mux *http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	register(mux)

	var inner http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			if idx := strings.IndexByte(pattern, ' '); idx >= 0 {
				pattern = pattern[idx+1:]
			}
			RecordRoutePattern(r.Context(), pattern)
		}
		mux.ServeHTTP(w, r)
	})

	for i := len(mws) - 1; i >= 0; i-- {
		inner = mws[i](inner)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithRoutePatternRecorder(r.Context())
		inner.ServeHTTP(w, r.WithContext(ctx))
	})
}
