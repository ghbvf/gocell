package query

import (
	"bytes"
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
