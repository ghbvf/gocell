package httputil

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/query"
)

// ParsePageParamsOrWrite parses pagination query params from r.
// On error it logs a warning, writes the domain error response, and returns ok=false.
// The caller must return immediately when ok is false.
func ParsePageParamsOrWrite(w http.ResponseWriter, r *http.Request) (query.PageParams, bool) {
	params, err := ParsePageParams(r)
	if err != nil {
		slog.Warn("pagination: request validation failed",
			slog.Any("error", err),
			slog.String("path", r.URL.Path),
		)
		WriteDomainError(r.Context(), w, err)
		return query.PageParams{}, false
	}
	return params, true
}
