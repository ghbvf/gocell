package query

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
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
	// RunMode controls fail-open vs fail-closed semantics. Zero value is
	// RunModeProd (fail-closed) so callers who forget to set this get strict
	// cursor validation. RunModeDemo only absorbs decode failures (stale key
	// after restart); scope/context mismatches still return an error because
	// they indicate a client bug.
	//
	// ref: ThreeDotsLabs/watermill — noop is injected at call site, not
	// inferred from data. Callers declare the mode explicitly.
	RunMode RunMode
}

// ExecutePagedQuery normalizes the request, decodes and validates the cursor,
// calls Fetch, and builds the paginated result. It replaces the ~15-line
// pattern repeated across 5 service List methods.
func ExecutePagedQuery[T any](ctx context.Context, cfg PagedQueryConfig[T]) (PageResult[T], error) {
	if cfg.Codec == nil || cfg.Fetch == nil || cfg.Extract == nil {
		return PageResult[T]{}, errcode.New(errcode.ErrInternal,
			"paged query misconfigured: Codec, Fetch, and Extract must not be nil")
	}
	cfg.Request.Normalize()

	cursorValues, fallback, err := resolveCursor(ctx, cfg)
	if err != nil {
		return PageResult[T]{}, err
	}
	if fallback {
		return fetchFirstPage(ctx, cfg)
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

// resolveCursor decodes and validates the cursor token. Returns the keyset
// values for the next page, or (nil, true, nil) when RunMode is Demo and a
// decode failure should fall back to the first page.
//
// RunModeDemo only absorbs decode failures (stale key after server restart).
// Scope/context mismatches always return an error because they indicate a
// client bug (cross-endpoint cursor reuse), not a transient key issue.
func resolveCursor[T any](ctx context.Context, cfg PagedQueryConfig[T]) ([]any, bool, error) {
	if cfg.Request.Cursor == "" {
		return nil, false, nil
	}

	cur, err := cfg.Codec.Decode(cfg.Request.Cursor)
	if err != nil {
		reportCursorErr(ctx, cfg.OnCursorErr, CursorPhaseDecode, err)
		if cfg.RunMode.IsDemo() {
			return nil, true, nil
		}
		return nil, false, err
	}

	if err := ValidateCursorScope(cur, cfg.Sort, cfg.QueryCtx); err != nil {
		reportCursorErr(ctx, cfg.OnCursorErr, CursorPhaseScope, err)
		return nil, false, err
	}

	return cur.Values, false, nil
}

func reportCursorErr(ctx context.Context, fn CursorErrorFunc, phase string, err error) {
	if fn != nil {
		fn(ctx, phase, err)
	}
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
