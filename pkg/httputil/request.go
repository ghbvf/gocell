package httputil

import (
	"fmt"
	"log/slog"
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
// Returns ErrCursorInvalid if the cursor exceeds query.MaxCursorTokenBytes —
// rejecting oversize cursors at the parse boundary bounds the work any handler
// can be forced to do before the codec's own length guard fires.
// ref: kubernetes apiserver 4 KiB continue-token guidance.
// Zero or negative limits are normalized to DefaultPageSize.
func ParsePageRequest(r *http.Request) (query.PageRequest, error) {
	var pr query.PageRequest

	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			slog.Warn("pagination: invalid limit parameter",
				slog.String("value", s),
			)
			return pr, errcode.New(errcode.ErrValidationFailed, "invalid limit parameter")
		}
		if n > query.MaxPageSize {
			slog.Warn("pagination: page size exceeded",
				slog.Int("requested", n),
				slog.Int("max", query.MaxPageSize),
			)
			return pr, errcode.New(errcode.ErrPageSizeExceeded,
				fmt.Sprintf("limit %d exceeds maximum %d", n, query.MaxPageSize))
		}
		pr.Limit = n
	}

	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > query.MaxCursorTokenBytes {
		slog.Warn("pagination: cursor token exceeds maximum length",
			slog.Int("length", len(cursor)),
			slog.Int("max", query.MaxCursorTokenBytes),
		)
		return pr, errcode.New(errcode.ErrCursorInvalid,
			"cursor token exceeds maximum length")
	}
	pr.Cursor = cursor
	pr.Normalize()
	return pr, nil
}
