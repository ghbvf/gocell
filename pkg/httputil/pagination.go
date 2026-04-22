package httputil

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/query"
)

// ParsePageRequestOrWrite parses pagination query params from r.
// On error it logs a warning, writes the domain error response, and returns ok=false.
// The caller must return immediately when ok is false.
func ParsePageRequestOrWrite(w http.ResponseWriter, r *http.Request) (query.PageRequest, bool) {
	pr, err := ParsePageRequest(r)
	if err != nil {
		slog.Warn("pagination: request validation failed",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path),
		)
		WriteDomainError(r.Context(), w, err)
		return query.PageRequest{}, false
	}
	return pr, true
}
