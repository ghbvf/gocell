package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// RuntimeCellIDSentinel is the framework "owner" used as the cell label for
// requests that do not belong to any cell-owned HTTP namespace.
const RuntimeCellIDSentinel = "_runtime"

// CellResolver maps a concrete request to the owning cell ID.
type CellResolver func(method, path string) (string, bool)

// CellAttribution writes the owning cell ID into request context before
// protection middleware can short-circuit. Requests with no resolved cell keep
// an absent ctx key; Metrics converts absence to RuntimeCellIDSentinel.
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
