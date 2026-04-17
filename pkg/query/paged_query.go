package query

import "context"

// CursorErrorFunc is called when cursor decode or scope validation fails.
// phase is "decode" or "scope".
type CursorErrorFunc func(ctx context.Context, phase string, err error)

// PagedQueryConfig holds all parameters for ExecutePagedQuery.
// ref: ent MultiCursorsOptions struct pattern; dispatcherConfig in kernel/assembly
type PagedQueryConfig[T any] struct {
	Codec       *CursorCodec
	Request     PageRequest
	Sort        []SortColumn
	QueryCtx    string
	Fetch       func(context.Context, ListParams) ([]T, error)
	Extract     func(T) []any
	OnCursorErr CursorErrorFunc
	DemoMode    bool
}

// ExecutePagedQuery normalizes the request, decodes and validates the cursor,
// calls Fetch, and builds the paginated result. It replaces the ~15-line
// pattern repeated across 5 service List methods.
func ExecutePagedQuery[T any](ctx context.Context, cfg PagedQueryConfig[T]) (PageResult[T], error) {
	cfg.Request.Normalize()

	var cursorValues []any
	if cfg.Request.Cursor != "" {
		cur, err := cfg.Codec.Decode(cfg.Request.Cursor)
		if err != nil {
			if cfg.OnCursorErr != nil {
				cfg.OnCursorErr(ctx, "decode", err)
			}
			if cfg.DemoMode {
				return fetchFirstPage(ctx, cfg)
			}
			return PageResult[T]{}, err
		}
		if err := ValidateCursorScope(cur, cfg.Sort, cfg.QueryCtx); err != nil {
			if cfg.OnCursorErr != nil {
				cfg.OnCursorErr(ctx, "scope", err)
			}
			if cfg.DemoMode {
				return fetchFirstPage(ctx, cfg)
			}
			return PageResult[T]{}, err
		}
		cursorValues = cur.Values
	}

	params := ListParams{
		Limit:        cfg.Request.Limit,
		CursorValues: cursorValues,
		Sort:         cfg.Sort,
	}

	items, err := cfg.Fetch(ctx, params)
	if err != nil {
		return PageResult[T]{}, err
	}

	return BuildPageResult(items, cfg.Request.Limit, cfg.Codec, cfg.Sort, cfg.QueryCtx, cfg.Extract)
}

func fetchFirstPage[T any](ctx context.Context, cfg PagedQueryConfig[T]) (PageResult[T], error) {
	params := ListParams{
		Limit: cfg.Request.Limit,
		Sort:  cfg.Sort,
	}
	items, err := cfg.Fetch(ctx, params)
	if err != nil {
		return PageResult[T]{}, err
	}
	return BuildPageResult(items, cfg.Request.Limit, cfg.Codec, cfg.Sort, cfg.QueryCtx, cfg.Extract)
}
