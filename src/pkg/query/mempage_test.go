package query_test

import (
	"testing"
	"time"

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

	result := query.ApplyCursor(items, params, testFieldValue)

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

	result := query.ApplyCursor(items, params, testFieldValue)

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

	result := query.ApplyCursor(items, params, testFieldValue)

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

	result := query.ApplyCursor(items, params, testFieldValue)

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

	result := query.ApplyCursor(items, params, testFieldValue)

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

	result := query.ApplyCursor(items, params, testFieldValue)

	require.Len(t, result, 2, "should skip item at cursor position")
	assert.Equal(t, "2", result[0].ID)
	assert.Equal(t, "3", result[1].ID)
}

// --- CompareAny tests ---

func TestCompareAny_StringVsString(t *testing.T) {
	assert.Equal(t, -1, query.CompareAny("a", "b"))
	assert.Equal(t, 0, query.CompareAny("x", "x"))
	assert.Equal(t, 1, query.CompareAny("z", "a"))
}

func TestCompareAny_Float64VsFloat64(t *testing.T) {
	assert.Equal(t, -1, query.CompareAny(1.0, 2.0))
	assert.Equal(t, 0, query.CompareAny(3.14, 3.14))
	assert.Equal(t, 1, query.CompareAny(9.9, 1.1))
}

func TestCompareAny_TimeVsTime(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Nanosecond)

	assert.Equal(t, -1, query.CompareAny(t1, t2))
	assert.Equal(t, 0, query.CompareAny(t1, t1))
	assert.Equal(t, 1, query.CompareAny(t2, t1))
}

func TestCompareAny_TimeVsString(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 100, time.UTC)
	s := t1.Format(time.RFC3339Nano)

	// time.Time vs string(RFC3339Nano) — should compare equal
	assert.Equal(t, 0, query.CompareAny(t1, s))

	// time.Time earlier than string
	earlier := t1.Add(-time.Second)
	assert.Equal(t, -1, query.CompareAny(earlier, s))

	// time.Time later than string
	later := t1.Add(time.Second)
	assert.Equal(t, 1, query.CompareAny(later, s))
}

func TestCompareAny_StringVsTime(t *testing.T) {
	t1 := time.Date(2026, 6, 15, 8, 30, 0, 500, time.UTC)
	s := t1.Format(time.RFC3339Nano)

	assert.Equal(t, 0, query.CompareAny(s, t1))
	assert.Equal(t, -1, query.CompareAny(s, t1.Add(time.Second)))
	assert.Equal(t, 1, query.CompareAny(s, t1.Add(-time.Second)))
}

func TestCompareAny_UnsupportedType_Panics(t *testing.T) {
	assert.Panics(t, func() {
		query.CompareAny(42, "str")
	})
	assert.Panics(t, func() {
		query.CompareAny(true, false)
	})
	assert.Panics(t, func() {
		query.CompareAny(1.0, "str")
	})
	assert.Panics(t, func() {
		query.CompareAny(1.0, time.Now())
	})
}

func TestApplyCursor_MismatchedCursorValuesLength_Panics(t *testing.T) {
	items := []testItem{{ID: "a"}, {ID: "b"}}
	params := query.ListParams{
		Limit:        10,
		CursorValues: []any{"a"}, // 1 value but 2 sort columns
		Sort: []query.SortColumn{
			{Name: "id", Direction: query.SortASC},
			{Name: "name", Direction: query.SortASC},
		},
	}

	// ApplyCursor does not validate length — this is enforced upstream by
	// ValidateCursorScope. If bypass occurs, it panics with index out of range.
	assert.Panics(t, func() {
		query.ApplyCursor(items, params, testFieldValue)
	})
}
