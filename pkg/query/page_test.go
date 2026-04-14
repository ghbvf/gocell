package query

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageRequest_Normalize_Default(t *testing.T) {
	var pr PageRequest
	pr.Normalize()
	assert.Equal(t, DefaultPageSize, pr.Limit)
}

func TestPageRequest_Normalize_ClampsMax(t *testing.T) {
	pr := PageRequest{Limit: 1000}
	pr.Normalize()
	assert.Equal(t, MaxPageSize, pr.Limit)
}

func TestPageRequest_Normalize_ClampsMin(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{"zero", 0},
		{"negative", -1},
		{"negative large", -100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PageRequest{Limit: tt.limit}
			pr.Normalize()
			assert.Equal(t, DefaultPageSize, pr.Limit)
		})
	}
}

func TestPageRequest_Normalize_KeepsValid(t *testing.T) {
	pr := PageRequest{Limit: 100}
	pr.Normalize()
	assert.Equal(t, 100, pr.Limit)
}

func TestPageRequest_Normalize_PreservesCursor(t *testing.T) {
	pr := PageRequest{Limit: 0, Cursor: "some-token"}
	pr.Normalize()
	assert.Equal(t, DefaultPageSize, pr.Limit)
	assert.Equal(t, "some-token", pr.Cursor)
}

func TestListParams_FetchLimit(t *testing.T) {
	lp := ListParams{Limit: 50}
	assert.Equal(t, 51, lp.FetchLimit())
}

func TestListParams_FetchLimit_One(t *testing.T) {
	lp := ListParams{Limit: 1}
	assert.Equal(t, 2, lp.FetchLimit())
}

func TestPageRequest_Normalize_ExactMax(t *testing.T) {
	pr := PageRequest{Limit: MaxPageSize}
	pr.Normalize()
	assert.Equal(t, MaxPageSize, pr.Limit)
}

var testSort = []SortColumn{{Name: "name", Direction: SortASC}}

func TestBuildPageResult_HasMore(t *testing.T) {
	codec, _ := NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	items := []string{"a", "b", "c", "d"} // 4 items, limit=3 → hasMore
	result, err := BuildPageResult(items, 3, codec, testSort, "test", func(s string) []any {
		return []any{s}
	})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
	assert.True(t, result.HasMore)
	assert.NotEmpty(t, result.NextCursor)
}

func TestBuildPageResult_LastPage(t *testing.T) {
	codec, _ := NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	items := []string{"a", "b"} // 2 items, limit=3 → no more
	result, err := BuildPageResult(items, 3, codec, testSort, "test", func(s string) []any {
		return []any{s}
	})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestBuildPageResult_Empty(t *testing.T) {
	codec, _ := NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	var items []string
	result, err := BuildPageResult(items, 10, codec, testSort, "test", func(s string) []any {
		return []any{s}
	})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.NotNil(t, result.Items) // must be [] not null
	assert.False(t, result.HasMore)
}

func TestBuildPageResult_ExactLimit(t *testing.T) {
	codec, _ := NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	items := []string{"a", "b", "c"} // 3 items, limit=3 → no more (exactly limit, not limit+1)
	result, err := BuildPageResult(items, 3, codec, testSort, "test", func(s string) []any {
		return []any{s}
	})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

// --- MapPageResult tests ---

func TestMapPageResult_Empty(t *testing.T) {
	src := PageResult[int]{Items: []int{}, HasMore: false}
	got := MapPageResult(src, func(i int) string { return "" })
	assert.Empty(t, got.Items)
	assert.NotNil(t, got.Items)
	assert.False(t, got.HasMore)
	assert.Empty(t, got.NextCursor)
}

func TestMapPageResult_Single(t *testing.T) {
	src := PageResult[int]{Items: []int{42}, HasMore: false}
	got := MapPageResult(src, func(i int) string {
		return "val-" + string(rune('0'+i%10))
	})
	require.Len(t, got.Items, 1)
	assert.Equal(t, "val-2", got.Items[0]) // 42 % 10 = 2
}

func TestMapPageResult_Multiple_PreservesCursor(t *testing.T) {
	src := PageResult[int]{
		Items:      []int{1, 2, 3},
		NextCursor: "cursor-token-abc",
		HasMore:    true,
	}
	got := MapPageResult(src, func(i int) int { return i * 10 })
	assert.Equal(t, []int{10, 20, 30}, got.Items)
	assert.Equal(t, "cursor-token-abc", got.NextCursor)
	assert.True(t, got.HasMore)
}

func TestMapPageResult_NilItems(t *testing.T) {
	src := PageResult[int]{Items: nil, HasMore: false}
	got := MapPageResult(src, func(i int) string { return "" })
	assert.NotNil(t, got.Items)
	assert.Empty(t, got.Items)
}

func TestMapPageResult_NilItems_JSONArray(t *testing.T) {
	src := PageResult[int]{Items: nil, HasMore: false}
	got := MapPageResult(src, func(i int) string { return "" })

	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"data":[]`)
	assert.NotContains(t, string(b), `"data":null`)
}
