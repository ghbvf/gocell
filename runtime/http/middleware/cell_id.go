package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// CellResolver maps a concrete request to the owning cell ID.
type CellResolver func(method, path string) (string, bool)

// CellAttribution writes the owning cell ID into request context before
// protection middleware can short-circuit. Requests with no resolved cell keep
// an absent ctx key; Metrics converts absence to the transport-neutral
// metrics.RuntimeCellSentinel ("_runtime") sentinel.
func CellAttribution(resolve CellResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if resolve == nil {
				next.ServeHTTP(w, r)
				return
			}
			cellID, ok := resolve(r.Method, r.URL.Path)
			if !ok || cellID == "" {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctxkeys.WithCellID(r.Context(), cellID)))
		})
	}
}
