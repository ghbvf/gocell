package query

const (
	// DefaultPageSize is the default number of items per page.
	DefaultPageSize = 50
	// MaxPageSize is the maximum allowed page size.
	MaxPageSize = 500
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
	Name      string // SQL column name, e.g. "created_at"
	Direction string // "ASC" or "DESC"
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
