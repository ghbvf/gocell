package query

import (
	"context"
	"fmt"
)

// CursorPhase identifies the cursor validation stage that failed.
const (
	CursorPhaseDecode = "decode"
	CursorPhaseScope  = "scope"
)

// CursorErrorFunc is called when cursor decode or scope validation fails.
type CursorErrorFunc func(ctx context.Context, phase string, err error)

// PagedQueryConfig holds all parameters for ExecutePagedQuery.
// ref: ent MultiCursorsOptions struct pattern; dispatcherConfig in kernel/assembly
type PagedQueryConfig[T any] struct {
	// Codec signs and verifies cursor tokens.
	Codec *CursorCodec
	// Request holds the client-supplied limit and cursor token.
	Request PageRequest
	// Sort defines the keyset column ordering for this query.
	Sort []SortColumn
	// QueryCtx is the fingerprint from QueryContext(); must match between page requests.
	QueryCtx string
	// Fetch retrieves items from the data store using the decoded ListParams.
	Fetch func(context.Context, ListParams) ([]T, error)
	// Extract returns cursor keyset values from the last item; count must match Sort.
	Extract func(T) []any
	// OnCursorErr is called when cursor decode or scope validation fails; nil is safe.
	OnCursorErr CursorErrorFunc
	// DemoMode when true causes cursor errors to fall back to the first page
	// instead of returning an error. Set to codec.IsDemoKey(KnownDemoKeys()...)
	// for automatic demo detection.
	DemoMode bool
}

// ExecutePagedQuery normalizes the request, decodes and validates the cursor,
// calls Fetch, and builds the paginated result. It replaces the ~15-line
// pattern repeated across 5 service List methods.
func ExecutePagedQuery[T any](ctx context.Context, cfg PagedQueryConfig[T]) (PageResult[T], error) {
	if cfg.Codec == nil || cfg.Fetch == nil || cfg.Extract == nil {
		return PageResult[T]{}, fmt.Errorf("paged query: Codec, Fetch, and Extract must not be nil")
	}
	cfg.Request.Normalize()

	var cursorValues []any
	if cfg.Request.Cursor != "" {
		cur, err := cfg.Codec.Decode(cfg.Request.Cursor)
		if err != nil {
			if cfg.OnCursorErr != nil {
				cfg.OnCursorErr(ctx, CursorPhaseDecode, err)
			}
			if cfg.DemoMode {
				return fetchFirstPage(ctx, cfg)
			}
			return PageResult[T]{}, err
		}
		if err := ValidateCursorScope(cur, cfg.Sort, cfg.QueryCtx); err != nil {
			if cfg.OnCursorErr != nil {
				cfg.OnCursorErr(ctx, CursorPhaseScope, err)
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
