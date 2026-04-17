package query

import (
	"context"
	"fmt"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testItem struct {
	Name string
	ID   string
}

var pagedTestSort = []SortColumn{
	{Name: "name", Direction: SortASC},
	{Name: "id", Direction: SortASC},
}

func testExtract(item testItem) []any {
	return []any{item.Name, item.ID}
}

func testFieldValue(item testItem, field string) any {
	switch field {
	case "name":
		return item.Name
	case "id":
		return item.ID
	default:
		return nil
	}
}

func testCompareField(a, b testItem, field string) int {
	av := testFieldValue(a, field).(string)
	bv := testFieldValue(b, field).(string)
	if av < bv {
		return -1
	}
	if av > bv {
		return 1
	}
	return 0
}

func newTestCodec(t *testing.T) *CursorCodec {
	t.Helper()
	codec, err := NewCursorCodec([]byte("test-paged-query-cursor-key-32b!"))
	require.NoError(t, err)
	return codec
}

func makeFetcher(items []testItem) func(context.Context, ListParams) ([]testItem, error) {
	return func(_ context.Context, params ListParams) ([]testItem, error) {
		sorted := make([]testItem, len(items))
		copy(sorted, items)
		Sort(sorted, params.Sort, testCompareField)
		result, err := ApplyCursor(sorted, params, func(item testItem, field string) any {
			return testFieldValue(item, field)
		})
		if err != nil {
			return nil, err
		}
		return result, nil
	}
}

func TestExecutePagedQuery_FirstPage(t *testing.T) {
	codec := newTestCodec(t)
	items := []testItem{
		{Name: "apple", ID: "1"},
		{Name: "banana", ID: "2"},
		{Name: "cherry", ID: "3"},
		{Name: "date", ID: "4"},
		{Name: "elderberry", ID: "5"},
	}

	result, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec:    codec,
		Request:  PageRequest{Limit: 3},
		Sort:     pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"),
		Fetch:    makeFetcher(items),
		Extract:  testExtract,
	})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
	assert.True(t, result.HasMore)
	assert.NotEmpty(t, result.NextCursor)
}

func TestExecutePagedQuery_SecondPage(t *testing.T) {
	codec := newTestCodec(t)
	items := []testItem{
		{Name: "apple", ID: "1"},
		{Name: "banana", ID: "2"},
		{Name: "cherry", ID: "3"},
		{Name: "date", ID: "4"},
		{Name: "elderberry", ID: "5"},
	}
	qctx := QueryContext("endpoint", "test")

	page1, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 3}, Sort: pagedTestSort, QueryCtx: qctx,
		Fetch: makeFetcher(items), Extract: testExtract,
	})
	require.NoError(t, err)

	page2, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 3, Cursor: page1.NextCursor}, Sort: pagedTestSort, QueryCtx: qctx,
		Fetch: makeFetcher(items), Extract: testExtract,
	})
	require.NoError(t, err)
	assert.Len(t, page2.Items, 2)
	assert.False(t, page2.HasMore)
	assert.Empty(t, page2.NextCursor)
}

func TestExecutePagedQuery_LastPage_FewerThanLimit(t *testing.T) {
	codec := newTestCodec(t)
	items := []testItem{
		{Name: "apple", ID: "1"},
		{Name: "banana", ID: "2"},
	}

	result, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 10}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(items), Extract: testExtract,
	})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestExecutePagedQuery_Empty(t *testing.T) {
	codec := newTestCodec(t)

	result, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 10}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.False(t, result.HasMore)
}

func TestExecutePagedQuery_GarbageCursor(t *testing.T) {
	codec := newTestCodec(t)

	_, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Cursor: "garbage"}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestExecutePagedQuery_ScopeMismatch(t *testing.T) {
	codec := newTestCodec(t)
	differentSort := []SortColumn{{Name: "other", Direction: SortDESC}}
	cur := Cursor{
		Values:  []any{"v1"},
		Scope:   SortScope(differentSort),
		Context: QueryContext("endpoint", "test"),
	}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	_, err = ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Cursor: token}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "sort scope mismatch", ecErr.Details["reason"])
}

func TestExecutePagedQuery_ContextMismatch(t *testing.T) {
	codec := newTestCodec(t)
	cur := Cursor{
		Values:  []any{"v1", "v2"},
		Scope:   SortScope(pagedTestSort),
		Context: QueryContext("endpoint", "wrong-endpoint"),
	}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	_, err = ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Cursor: token}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "query context mismatch", ecErr.Details["reason"])
}

func TestExecutePagedQuery_OnCursorErr_Decode(t *testing.T) {
	codec := newTestCodec(t)
	var capturedPhase string
	var capturedErr error

	_, _ = ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Cursor: "garbage"}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
		OnCursorErr: func(_ context.Context, phase string, err error) {
			capturedPhase = phase
			capturedErr = err
		},
	})

	assert.Equal(t, "decode", capturedPhase)
	assert.NotNil(t, capturedErr)
}

func TestExecutePagedQuery_OnCursorErr_Scope(t *testing.T) {
	codec := newTestCodec(t)
	differentSort := []SortColumn{{Name: "other", Direction: SortDESC}}
	cur := Cursor{
		Values:  []any{"v1"},
		Scope:   SortScope(differentSort),
		Context: QueryContext("endpoint", "test"),
	}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	var capturedPhase string
	_, _ = ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Cursor: token}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
		OnCursorErr: func(_ context.Context, phase string, _ error) {
			capturedPhase = phase
		},
	})

	assert.Equal(t, "scope", capturedPhase)
}

func TestExecutePagedQuery_OnCursorErr_NilDoesNotPanic(t *testing.T) {
	codec := newTestCodec(t)

	_, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Cursor: "garbage"}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
		OnCursorErr: nil,
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestExecutePagedQuery_FetchErrorPropagated(t *testing.T) {
	codec := newTestCodec(t)

	_, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 10}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"),
		Fetch: func(context.Context, ListParams) ([]testItem, error) {
			return nil, fmt.Errorf("db connection refused")
		},
		Extract: testExtract,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db connection refused")
}

func TestExecutePagedQuery_NormalizesLimit(t *testing.T) {
	codec := newTestCodec(t)
	items := make([]testItem, 60)
	for i := range items {
		items[i] = testItem{Name: fmt.Sprintf("item-%03d", i), ID: fmt.Sprintf("%03d", i)}
	}

	result, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 0}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(items), Extract: testExtract,
	})
	require.NoError(t, err)
	assert.Len(t, result.Items, DefaultPageSize)
}

func TestExecutePagedQuery_RunModeDemo_StaleCursor_ReturnsFirstPage(t *testing.T) {
	codec := newTestCodec(t)
	items := []testItem{
		{Name: "apple", ID: "1"},
		{Name: "banana", ID: "2"},
	}

	result, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 10, Cursor: "garbage"}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(items), Extract: testExtract,
		RunMode: RunModeDemo,
	})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
}

// TestExecutePagedQuery_RunModeProd_StaleCursor_ReturnsError guards the
// fail-closed default: with RunMode unset (zero value = Prod) a garbage
// cursor must return ErrCursorInvalid, not fall back to the first page.
// ref: kubernetes/kubernetes apiserver storage/continue.go — decode failure → 400
// ref: pilagod/gorm-cursor-paginator — decode failure → ErrInvalidCursor
func TestExecutePagedQuery_RunModeProd_StaleCursor_ReturnsError(t *testing.T) {
	codec := newTestCodec(t)

	_, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Cursor: "garbage"}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
		// RunMode unset — zero value must be RunModeProd (fail-closed).
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

// TestExecutePagedQuery_RunModeDemo_ScopeMismatch_StillRejects asserts that
// demo mode only absorbs decode failures, not scope/context mismatches.
// Scope mismatch indicates a client bug (cross-endpoint cursor reuse), which
// must surface as an error even in demo mode.
func TestExecutePagedQuery_RunModeDemo_ScopeMismatch_StillRejects(t *testing.T) {
	codec := newTestCodec(t)
	differentSort := []SortColumn{{Name: "other", Direction: SortDESC}}
	cur := Cursor{
		Values:  []any{"v1"},
		Scope:   SortScope(differentSort),
		Context: QueryContext("endpoint", "test"),
	}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	_, err = ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 10, Cursor: token}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"), Fetch: makeFetcher(nil), Extract: testExtract,
		RunMode: RunModeDemo,
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestExecutePagedQuery_RunModeDemo_FetchError_Propagated(t *testing.T) {
	codec := newTestCodec(t)

	_, err := ExecutePagedQuery(context.Background(), PagedQueryConfig[testItem]{
		Codec: codec, Request: PageRequest{Limit: 10, Cursor: "garbage"}, Sort: pagedTestSort,
		QueryCtx: QueryContext("endpoint", "test"),
		Fetch: func(context.Context, ListParams) ([]testItem, error) {
			return nil, fmt.Errorf("db connection refused")
		},
		Extract: testExtract,
		RunMode: RunModeDemo,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db connection refused")
}

// TestExecutePagedQuery_NilDependencies asserts the helper returns an
// errcode.ErrInternal (not a bare fmt.Errorf) when required fields are missing.
func TestExecutePagedQuery_NilDependencies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  PagedQueryConfig[testItem]
	}{
		{
			name: "nil Codec",
			cfg: PagedQueryConfig[testItem]{
				Fetch:   makeFetcher(nil),
				Extract: testExtract,
			},
		},
		{
			name: "nil Fetch",
			cfg: PagedQueryConfig[testItem]{
				Codec:   newTestCodec(t),
				Extract: testExtract,
			},
		},
		{
			name: "nil Extract",
			cfg: PagedQueryConfig[testItem]{
				Codec: newTestCodec(t),
				Fetch: makeFetcher(nil),
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ExecutePagedQuery(context.Background(), tc.cfg)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.ErrInternal, ecErr.Code)
		})
	}
}
