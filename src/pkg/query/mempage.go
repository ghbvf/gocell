package query

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// CompareFunc compares a single named field of two entities, returning -1/0/+1.
type CompareFunc[T any] func(a, b T, field string) int

// FieldFunc extracts a cursor-comparable value from an entity by field name.
// Returned values must be string, int, float64, or time.Time — other types
// will cause CompareAny to return an error. Time fields should return
// time.Time (not a formatted string) so that CompareAny uses temporal
// comparison.
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
//
// Precondition: items must already be sorted by params.Sort columns (via Sort).
// Behavior is undefined on unsorted input.
//
// Returns ErrCursorInvalid if CursorValues length does not match Sort columns,
// if Sort is empty when CursorValues is present, or if cursor value types are
// incompatible.
func ApplyCursor[T any](items []T, params ListParams, fieldValue FieldFunc[T]) ([]T, error) {
	if params.CursorValues != nil {
		if len(params.Sort) == 0 {
			return nil, errcode.New(errcode.ErrCursorInvalid,
				"cursor values present but no sort columns defined")
		}
		if len(params.CursorValues) != len(params.Sort) {
			return nil, errcode.New(errcode.ErrCursorInvalid,
				fmt.Sprintf("cursor values count %d does not match sort columns count %d",
					len(params.CursorValues), len(params.Sort)))
		}
	}

	start := 0
	if params.CursorValues != nil {
		for i, item := range items {
			after, err := afterCursor(item, params.Sort, params.CursorValues, fieldValue)
			if err != nil {
				return nil, err
			}
			if after {
				start = i
				break
			}
			if i == len(items)-1 {
				start = len(items) // cursor past all rows
			}
		}
	}

	end := min(start+params.FetchLimit(), len(items))
	return items[start:end], nil
}

// afterCursor returns true if item is strictly after the cursor position
// according to the sort columns and their directions.
//
// Algorithm: multi-column lexicographic comparison with direction-awareness.
// For each column from first to last: compare item value vs cursor value.
// - Non-last column, values differ: result determined by direction (ASC→positive, DESC→negative).
// - Non-last column, values equal: continue to next column.
// - Last column: strict inequality required (excludes the cursor item itself).
func afterCursor[T any](item T, cols []SortColumn, cursorValues []any, fieldValue FieldFunc[T]) (bool, error) {
	for level := range len(cols) {
		val := fieldValue(item, cols[level].Name)
		curVal := cursorValues[level]
		c, err := CompareAny(val, curVal)
		if err != nil {
			return false, err
		}

		if level < len(cols)-1 {
			if c != 0 {
				if cols[level].Direction == SortDESC {
					return c < 0, nil
				}
				return c > 0, nil
			}
			continue
		}
		// Last column: strict inequality.
		if cols[level].Direction == SortDESC {
			return c < 0, nil
		}
		return c > 0, nil
	}
	return false, nil
}

// CompareAny compares two values that may be string, float64, or time.Time.
// It handles cross-type comparison between time.Time and RFC3339Nano strings,
// which occurs when fieldValue returns time.Time but cursor values are strings
// from JSON decode.
//
// Supported type pairs: string↔string, float64↔float64, int↔float64,
// time.Time↔time.Time, time.Time↔string (parsed as RFC3339Nano).
// All other combinations return ErrCursorInvalid.
func CompareAny(a, b any) (int, error) {
	a, b = normalizeNumeric(a), normalizeNumeric(b)

	switch av := a.(type) {
	case string:
		if bv, ok := b.(string); ok {
			return cmp.Compare(av, bv), nil
		}
		if bt, ok := b.(time.Time); ok {
			at, err := parseTimeString(av)
			if err != nil {
				return 0, err
			}
			return at.Compare(bt), nil
		}
	case float64:
		if bv, ok := b.(float64); ok {
			return cmp.Compare(av, bv), nil
		}
	case time.Time:
		if bt, ok := b.(time.Time); ok {
			return av.Compare(bt), nil
		}
		if bs, ok := b.(string); ok {
			bt, err := parseTimeString(bs)
			if err != nil {
				return 0, err
			}
			return av.Compare(bt), nil
		}
	}

	return 0, errcode.New(errcode.ErrCursorInvalid, "invalid cursor value")
}

// normalizeNumeric converts int to float64 for uniform numeric comparison.
// JSON decode produces float64, but Go struct fields often use int.
func normalizeNumeric(v any) any {
	if i, ok := v.(int); ok {
		return float64(i)
	}
	return v
}

// parseTimeString parses s as RFC3339Nano, returning ErrCursorInvalid on failure.
func parseTimeString(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, errcode.New(errcode.ErrCursorInvalid, "invalid cursor value")
	}
	return t, nil
}
