package httputil

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// ParsePageRequest extracts pagination parameters from URL query params.
// Query params: ?limit=N&cursor=TOKEN
//
// Returns ErrPageSizeExceeded if limit > MaxPageSize.
// Returns ErrValidationFailed if limit is not a valid integer.
// Zero or negative limits are normalized to DefaultPageSize.
func ParsePageRequest(r *http.Request) (query.PageRequest, error) {
	var pr query.PageRequest

	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return pr, errcode.New(errcode.ErrValidationFailed, "invalid limit parameter")
		}
		if n > query.MaxPageSize {
			return pr, errcode.New(errcode.ErrPageSizeExceeded,
				fmt.Sprintf("limit %d exceeds maximum %d", n, query.MaxPageSize))
		}
		pr.Limit = n
	}

	pr.Cursor = r.URL.Query().Get("cursor")
	pr.Normalize()
	return pr, nil
}
