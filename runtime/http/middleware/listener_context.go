package middleware

import (
	"context"
	"net/http"
)

type listenerContextKey struct{}

// ListenerContext annotates every request with the physical listener name.
// Empty names are ignored so zero-ref test routers and standalone middleware
// do not emit a blank listener field.
func ListenerContext(name string) func(http.Handler) http.Handler {
	if name == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), listenerContextKey{}, name)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func listenerFromContext(ctx context.Context) (string, bool) {
	name, ok := ctx.Value(listenerContextKey{}).(string)
	return name, ok && name != ""
}
