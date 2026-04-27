package httputil

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/pkg/query"
)

// ParsePageParamsOrWrite parses pagination query params from r.
// On error it writes the domain error response using the request's logging
// policy and returns ok=false. The caller must return immediately when ok is
// false.
func ParsePageParamsOrWrite(w http.ResponseWriter, r *http.Request) (query.PageParams, bool) {
	params, err := ParsePageParams(r)
	if err != nil {
		WritePageDomainError(r.Context(), w, err)
		return query.PageParams{}, false
	}
	return params, true
}

// WritePageDomainError writes a pagination/list-domain error. List handlers
// apply sampling at the request boundary with WithListErrorLogSampling; response
// shape and status mapping stay identical to WriteDomainError.
func WritePageDomainError(ctx context.Context, w http.ResponseWriter, err error) {
	WriteDomainError(ctx, w, err)
}
