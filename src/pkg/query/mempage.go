package query

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// CompareFunc compares a single named field of two entities, returning -1/0/+1.
type CompareFunc[T any] func(a, b T, field string) int

// FieldFunc extracts a cursor-comparable value from an entity by field name.
// Returned values must be string, float64, or time.Time — other types will
// cause CompareAny to panic. Time fields should return time.Time (not a
// formatted string) so that CompareAny uses temporal comparison.
type FieldFunc[T any] func(item T, field string) any

// Sort sorts items in-place by the given sort columns using compareField.
func Sort[T any](items []T, cols []SortColumn, compareField CompareFunc[T]) {
	if len(cols) == 0 {
		return
	}
	slices.SortFunc(items, func(a, b T) int {
		for _, col := range cols {
			v := compareField(a, b, col.Name)
			if col.Direction == SortDESC {
				v = -v
			}
			if v != 0 {
				return v
			}
		}
		return 0
	})
}

// ApplyCursor skips items at or before the cursor position, then limits to
// FetchLimit() (Limit+1 for N+1 hasMore detection).
func ApplyCursor[T any](items []T, params ListParams, fieldValue FieldFunc[T]) []T {
	start := 0
	if params.CursorValues != nil {
		for i, item := range items {
			if afterCursor(item, params.Sort, params.CursorValues, fieldValue) {
				start = i
				break
			}
			if i == len(items)-1 {
				start = len(items) // cursor past all rows
			}
		}
	}

	end := min(start+params.FetchLimit(), len(items))
	return items[start:end]
}

// afterCursor returns true if item is strictly after the cursor position
// according to the sort columns and their directions.
//
// Algorithm: multi-column lexicographic comparison with direction-awareness.
// For each column from first to last: compare item value vs cursor value.
// - Non-last column, values differ: result determined by direction (ASC→positive, DESC→negative).
// - Non-last column, values equal: continue to next column.
// - Last column: strict inequality required (excludes the cursor item itself).
func afterCursor[T any](item T, cols []SortColumn, cursorValues []any, fieldValue FieldFunc[T]) bool {
	for level := range len(cols) {
		val := fieldValue(item, cols[level].Name)
		curVal := cursorValues[level]
		c := CompareAny(val, curVal)

		if level < len(cols)-1 {
			if c != 0 {
				if cols[level].Direction == SortDESC {
					return c < 0
				}
				return c > 0
			}
			continue
		}
		// Last column: strict inequality.
		if cols[level].Direction == SortDESC {
			return c < 0
		}
		return c > 0
	}
	return false
}

// CompareAny compares two values that may be string, float64, or time.Time.
// It handles cross-type comparison between time.Time and RFC3339Nano strings,
// which occurs when fieldValue returns time.Time but cursor values are strings
// from JSON decode.
//
// Supported type pairs: string↔string, float64↔float64, time.Time↔time.Time,
// time.Time↔string (parsed as RFC3339Nano). All other combinations panic.
// This is safe because inputs come from HMAC-validated cursor values (JSON
// decode produces only string/float64) and FieldFunc callbacks (returning
// string/float64/time.Time).
func CompareAny(a, b any) int {
	// Fast path: both same concrete type.
	switch av := a.(type) {
	case string:
		if bv, ok := b.(string); ok {
			return cmp.Compare(av, bv)
		}
		if bt, ok := b.(time.Time); ok {
			at, err := time.Parse(time.RFC3339Nano, av)
			if err != nil {
				panic(fmt.Sprintf("CompareAny: cannot parse string %q as RFC3339Nano: %v", av, err))
			}
			return at.Compare(bt)
		}
	case float64:
		if bv, ok := b.(float64); ok {
			return cmp.Compare(av, bv)
		}
	case time.Time:
		if bt, ok := b.(time.Time); ok {
			return av.Compare(bt)
		}
		if bs, ok := b.(string); ok {
			bt, err := time.Parse(time.RFC3339Nano, bs)
			if err != nil {
				panic(fmt.Sprintf("CompareAny: cannot parse string %q as RFC3339Nano: %v", bs, err))
			}
			return av.Compare(bt)
		}
	}

	panic(fmt.Sprintf("CompareAny: unsupported type combination %T vs %T", a, b))
}
