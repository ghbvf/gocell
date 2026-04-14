package query

const (
	// DefaultPageSize is the default number of items per page.
	DefaultPageSize = 50
	// MaxPageSize is the maximum allowed page size.
	MaxPageSize = 500
)

// SortDir represents a sort direction.
type SortDir string

const (
	// SortASC sorts ascending.
	SortASC SortDir = "ASC"
	// SortDESC sorts descending.
	SortDESC SortDir = "DESC"
)

// PageRequest holds pagination parameters parsed from an HTTP request.
type PageRequest struct {
	Limit  int    // requested page size
	Cursor string // opaque cursor token; empty for first page
}

// Normalize clamps Limit to [1, MaxPageSize], applying DefaultPageSize
// for zero or negative values.
func (p *PageRequest) Normalize() {
	if p.Limit <= 0 {
		p.Limit = DefaultPageSize
	}
	if p.Limit > MaxPageSize {
		p.Limit = MaxPageSize
	}
}

// SortColumn defines a column used in keyset ordering.
type SortColumn struct {
	Name      string  // SQL column name — must be a trusted identifier, never user input
	Direction SortDir // SortASC or SortDESC
}

// ListParams holds pagination parameters for repository list operations.
// Passed from the service layer (after cursor decoding) to the repository.
type ListParams struct {
	Limit        int          // user-requested limit (normalized)
	CursorValues []any        // decoded cursor keyset values; nil for first page
	Sort         []SortColumn // sort columns in order of priority
}

// FetchLimit returns Limit+1 for the N+1 hasMore detection pattern.
func (lp ListParams) FetchLimit() int {
	return lp.Limit + 1
}

// PageResult wraps a page of results with cursor metadata.
type PageResult[T any] struct {
	Items      []T    `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
	HasMore    bool   `json:"hasMore"`
}

// BuildPageResult processes raw query results (which may contain limit+1 rows
// for N+1 hasMore detection), trims to the requested limit, and encodes the
// cursor for the next page from the last visible item.
//
// sort defines the sort columns; the generated cursor embeds a sort scope
// fingerprint so cursors cannot be reused across different sort definitions.
//
// queryCtx is the query context fingerprint (from QueryContext). The cursor
// embeds this so it cannot be replayed against a different query context
// (e.g. different endpoint, different filter values).
//
// extractCursor is called on the last item to extract the keyset values for
// the next-page cursor. It must return values corresponding 1:1 to the sort
// columns used in the query.
func BuildPageResult[T any](items []T, limit int, codec *CursorCodec, sort []SortColumn, queryCtx string, extractCursor func(T) []any) (PageResult[T], error) {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var result PageResult[T]
	result.Items = items
	result.HasMore = hasMore

	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := Cursor{Values: extractCursor(last), Scope: SortScope(sort), Context: queryCtx}
		token, err := codec.Encode(cur)
		if err != nil {
			return PageResult[T]{}, err
		}
		result.NextCursor = token
	}

	if result.Items == nil {
		// Ensure JSON serializes as [] not null.
		result.Items = make([]T, 0)
	}

	return result, nil
}

// MapPageResult transforms each item in a PageResult using fn, preserving
// pagination metadata (NextCursor, HasMore). fn must not panic; if an item
// may be nil, the caller should handle that within fn.
func MapPageResult[T any, U any](src PageResult[T], fn func(T) U) PageResult[U] {
	if len(src.Items) == 0 {
		return PageResult[U]{
			Items:      make([]U, 0),
			NextCursor: src.NextCursor,
			HasMore:    src.HasMore,
		}
	}
	items := make([]U, len(src.Items))
	for i, item := range src.Items {
		items[i] = fn(item)
	}
	return PageResult[U]{
		Items:      items,
		NextCursor: src.NextCursor,
		HasMore:    src.HasMore,
	}
}
