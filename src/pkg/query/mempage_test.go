package query_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test helpers ---

type testItem struct {
	ID        string
	Name      string
	Score     float64
	CreatedAt time.Time
}

func compareTestField(a, b testItem, field string) int {
	switch field {
	case "id":
		return cmpStr(a.ID, b.ID)
	case "name":
		return cmpStr(a.Name, b.Name)
	case "score":
		return cmpFloat(a.Score, b.Score)
	case "created_at":
		return a.CreatedAt.Compare(b.CreatedAt)
	default:
		return 0
	}
}

func testFieldValue(item testItem, field string) any {
	switch field {
	case "id":
		return item.ID
	case "name":
		return item.Name
	case "score":
		return item.Score
	case "created_at":
		return item.CreatedAt
	default:
		return ""
	}
}

func cmpStr(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// requireCursorInvalidMsg asserts the error is a standardized cursor error
// with unified message and the expected reason in details.
// NOTE: the string literal must match query.cursorInvalidMsg (unexported).
func requireCursorInvalidMsg(t *testing.T, err error, wantReason string) {
	t.Helper()
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "invalid cursor; restart from first page", ecErr.Message,
		"client-facing message must be stable across all cursor errors")
	assert.Equal(t, wantReason, ecErr.Details["reason"])
}

// --- Sort tests ---

func TestSort_SingleColumn_ASC(t *testing.T) {
	items := []testItem{
		{ID: "c"}, {ID: "a"}, {ID: "b"},
	}
	cols := []query.SortColumn{{Name: "id", Direction: query.SortASC}}

	query.Sort(items, cols, compareTestField)

	assert.Equal(t, "a", items[0].ID)
	assert.Equal(t, "b", items[1].ID)
	assert.Equal(t, "c", items[2].ID)
}

func TestSort_SingleColumn_DESC(t *testing.T) {
	items := []testItem{
		{ID: "a"}, {ID: "c"}, {ID: "b"},
	}
	cols := []query.SortColumn{{Name: "id", Direction: query.SortDESC}}

	query.Sort(items, cols, compareTestField)

	assert.Equal(t, "c", items[0].ID)
	assert.Equal(t, "b", items[1].ID)
	assert.Equal(t, "a", items[2].ID)
}

func TestSort_MultiColumn(t *testing.T) {
	items := []testItem{
		{Name: "bob", ID: "2"},
		{Name: "alice", ID: "3"},
		{Name: "alice", ID: "1"},
	}
	cols := []query.SortColumn{
		{Name: "name", Direction: query.SortASC},
		{Name: "id", Direction: query.SortASC},
	}

	query.Sort(items, cols, compareTestField)

	assert.Equal(t, "alice", items[0].Name)
	assert.Equal(t, "1", items[0].ID)
	assert.Equal(t, "alice", items[1].Name)
	assert.Equal(t, "3", items[1].ID)
	assert.Equal(t, "bob", items[2].Name)
}

func TestSort_EmptyItems(t *testing.T) {
	var items []testItem
	cols := []query.SortColumn{{Name: "id", Direction: query.SortASC}}
	query.Sort(items, cols, compareTestField)
	assert.Empty(t, items)
}

func TestSort_EmptyColumns(t *testing.T) {
	items := []testItem{{ID: "b"}, {ID: "a"}}
	query.Sort(items, nil, compareTestField)
	// order unchanged
	assert.Equal(t, "b", items[0].ID)
	assert.Equal(t, "a", items[1].ID)
}

// --- ApplyCursor tests ---

func TestApplyCursor_FirstPage(t *testing.T) {
	items := []testItem{
		{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
	}
	params := query.ListParams{
		Limit: 2,
		Sort:  []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}

	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)

	// FetchLimit = 3, so we get 3 items (for hasMore detection)
	require.Len(t, result, 3)
	assert.Equal(t, "a", result[0].ID)
	assert.Equal(t, "b", result[1].ID)
	assert.Equal(t, "c", result[2].ID)
}

func TestApplyCursor_WithCursor(t *testing.T) {
	items := []testItem{
		{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
	}
	params := query.ListParams{
		Limit:        2,
		CursorValues: []any{"b"},
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}

	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)

	require.Len(t, result, 2)
	assert.Equal(t, "c", result[0].ID)
	assert.Equal(t, "d", result[1].ID)
}

func TestApplyCursor_CursorPastEnd(t *testing.T) {
	items := []testItem{
		{ID: "a"}, {ID: "b"},
	}
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"z"},
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}

	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestApplyCursor_MultiColumnCursor(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []testItem{
		{ID: "1", CreatedAt: base},
		{ID: "2", CreatedAt: base},
		{ID: "3", CreatedAt: base.Add(time.Second)},
		{ID: "4", CreatedAt: base.Add(time.Second)},
	}
	// Sort: created_at ASC, id ASC. Cursor at (base, "1") → skip item 1.
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{base, "1"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}

	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)

	require.Len(t, result, 3)
	assert.Equal(t, "2", result[0].ID)
	assert.Equal(t, "3", result[1].ID)
	assert.Equal(t, "4", result[2].ID)
}

func TestApplyCursor_DESC_Direction(t *testing.T) {
	items := []testItem{
		{ID: "d"}, {ID: "c"}, {ID: "b"}, {ID: "a"},
	}
	// Sorted DESC. Cursor at "c" → next items are b, a.
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"c"},
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortDESC}},
	}

	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)

	require.Len(t, result, 2)
	assert.Equal(t, "b", result[0].ID)
	assert.Equal(t, "a", result[1].ID)
}

func TestApplyCursor_TimeVsString_CrossType(t *testing.T) {
	// This is the core CURSOR-P1-02 fix scenario:
	// fieldValue returns time.Time, cursor values contain RFC3339Nano strings.
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []testItem{
		{ID: "1", CreatedAt: base},
		{ID: "2", CreatedAt: base.Add(100 * time.Nanosecond)},
		{ID: "3", CreatedAt: base.Add(200 * time.Nanosecond)},
	}

	// Cursor value is a string (as it would be after JSON decode from cursor token).
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{base.Format(time.RFC3339Nano), "1"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}

	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)

	require.Len(t, result, 2, "should skip item at cursor position")
	assert.Equal(t, "2", result[0].ID)
	assert.Equal(t, "3", result[1].ID)
}

func TestApplyCursor_EmptyItems_WithCursor(t *testing.T) {
	var items []testItem
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"x"},
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}
	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestApplyCursor_MultiColumn_MixedDirection(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []testItem{
		{ID: "1", CreatedAt: base.Add(2 * time.Second)},
		{ID: "2", CreatedAt: base.Add(2 * time.Second)},
		{ID: "3", CreatedAt: base.Add(time.Second)},
		{ID: "4", CreatedAt: base.Add(time.Second)},
		{ID: "5", CreatedAt: base},
	}
	// Sort: created_at DESC, id ASC. Cursor at (base+2s, "1").
	// After cursor: items with same timestamp and id > "1", or earlier timestamps.
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{base.Add(2 * time.Second), "1"},
		Sort: []query.SortColumn{
			{Name: "created_at", Direction: query.SortDESC},
			{Name: "id", Direction: query.SortASC},
		},
	}

	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)
	require.Len(t, result, 4)
	assert.Equal(t, "2", result[0].ID) // same second, id > "1"
	assert.Equal(t, "3", result[1].ID)
	assert.Equal(t, "4", result[2].ID)
	assert.Equal(t, "5", result[3].ID)
}

func TestApplyCursor_AllItemsEqualCursor(t *testing.T) {
	items := []testItem{{ID: "a"}, {ID: "a"}, {ID: "a"}}
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"a"},
		Sort:         []query.SortColumn{{Name: "id", Direction: query.SortASC}},
	}
	result, err := query.ApplyCursor(items, params, testFieldValue)
	require.NoError(t, err)
	assert.Empty(t, result, "all items at cursor position should be skipped")
}

func TestApplyCursor_EmptySortWithCursor_ReturnsError(t *testing.T) {
	items := []testItem{{ID: "a"}}
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"a"},
		Sort:         []query.SortColumn{},
	}
	_, err := query.ApplyCursor(items, params, testFieldValue)
	requireCursorInvalidMsg(t, err, "cursor values present but no sort columns defined")
}

func TestApplyCursor_MismatchedCursorValuesLength_ReturnsError(t *testing.T) {
	items := []testItem{{ID: "a"}, {ID: "b"}}
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"a"}, // 1 value but 2 sort columns
		Sort: []query.SortColumn{
			{Name: "id", Direction: query.SortASC},
			{Name: "name", Direction: query.SortASC},
		},
	}

	_, err := query.ApplyCursor(items, params, testFieldValue)
	requireCursorInvalidMsg(t, err, "cursor values count 1 does not match sort columns count 2")
}

// --- CompareAny tests (table-driven) ---

func TestCompareAny(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 100, time.UTC)
	t2 := time.Date(2026, 6, 15, 8, 30, 0, 500, time.UTC)

	tests := []struct {
		name string
		a, b any
		want int
	}{
		// string vs string
		{"string < string", "a", "b", -1},
		{"string == string", "x", "x", 0},
		{"string > string", "z", "a", 1},

		// float64 vs float64
		{"float64 < float64", 1.0, 2.0, -1},
		{"float64 == float64", 3.14, 3.14, 0},
		{"float64 > float64", 9.9, 1.1, 1},

		// time vs time
		{"time < time", t1, t1.Add(time.Nanosecond), -1},
		{"time == time", t1, t1, 0},
		{"time > time", t1.Add(time.Nanosecond), t1, 1},

		// time vs string (cross-type)
		{"time == string", t1, t1.Format(time.RFC3339Nano), 0},
		{"time < string", t1.Add(-time.Second), t1.Format(time.RFC3339Nano), -1},
		{"time > string", t1.Add(time.Second), t1.Format(time.RFC3339Nano), 1},

		// string vs time (cross-type, reverse)
		{"string == time", t2.Format(time.RFC3339Nano), t2, 0},
		{"string < time", t2.Format(time.RFC3339Nano), t2.Add(time.Second), -1},
		{"string > time", t2.Format(time.RFC3339Nano), t2.Add(-time.Second), 1},

		// int vs float64 (normalized)
		{"int < float64", 1, 2.0, -1},
		{"int == float64", 3, 3.0, 0},
		{"int > float64", 5, 2.0, 1},

		// float64 vs int (normalized)
		{"float64 < int", 1.0, 2, -1},
		{"float64 == int", 3.0, 3, 0},
		{"float64 > int", 5.0, 2, 1},

		// int vs int (both normalized to float64)
		{"int < int", 1, 2, -1},
		{"int == int", 3, 3, 0},
		{"int > int", 5, 2, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := query.CompareAny(tt.a, tt.b)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCompareAny_Error(t *testing.T) {
	tests := []struct {
		name       string
		a, b       any
		wantReason string
	}{
		{"nil vs string", nil, "x", "unsupported cursor value types"},
		{"string vs nil", "x", nil, "unsupported cursor value types"},
		{"bool vs bool", true, false, "unsupported cursor value types"},
		{"float64 vs string", 1.0, "str", "unsupported cursor value types"},
		{"float64 vs time", 1.0, time.Now(), "unsupported cursor value types"},
		{"time vs invalid string", time.Now(), "not-a-time", "invalid time format in cursor value"},
		{"invalid string vs time", "not-a-time", time.Now(), "invalid time format in cursor value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := query.CompareAny(tt.a, tt.b)
			requireCursorInvalidMsg(t, err, tt.wantReason)
		})
	}
}
