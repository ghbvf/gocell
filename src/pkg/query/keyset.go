package query

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// validColumnName matches safe SQL column identifiers.
var validColumnName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// AppendKeyset appends keyset pagination clauses (WHERE + ORDER BY + LIMIT)
// to a Builder. It integrates with any existing WHERE conditions via AND.
//
// When CursorValues is nil (first page), only ORDER BY and LIMIT are appended.
// When CursorValues is set, a keyset WHERE clause is generated:
//   - Same direction columns: tuple comparison (col1, col2) > ($1, $2)
//   - Mixed direction columns: compound OR
//
// LIMIT is set to params.FetchLimit() (Limit+1) for N+1 hasMore detection.
//
// Callers should ensure a composite index exists on the sort columns in the
// specified directions (e.g. CREATE INDEX idx ON table (col1 DESC, col2 ASC))
// for efficient keyset pagination. Without such an index, the database will
// perform a full table sort on every page request.
func AppendKeyset(b *Builder, params ListParams) error {
	if len(params.Sort) == 0 {
		return errcode.New(errcode.ErrValidationFailed, "keyset: at least one sort column is required")
	}

	for _, col := range params.Sort {
		if !validColumnName.MatchString(col.Name) {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("keyset: invalid column name %q", col.Name))
		}
		if col.Direction != SortASC && col.Direction != SortDESC {
			return errcode.New(errcode.ErrValidationFailed,
				fmt.Sprintf("keyset: invalid direction %q, must be ASC or DESC", col.Direction))
		}
	}

	if params.CursorValues != nil {
		if len(params.CursorValues) != len(params.Sort) {
			return errcode.New(errcode.ErrCursorInvalid,
				fmt.Sprintf("keyset: cursor has %d values but %d sort columns",
					len(params.CursorValues), len(params.Sort)))
		}
		if err := appendKeysetWhere(b, params.Sort, params.CursorValues); err != nil {
			return err
		}
	}

	appendOrderBy(b, params.Sort)
	b.AppendParam("LIMIT ", params.FetchLimit())

	return nil
}

// sameDirection returns true if all sort columns share the same direction.
func sameDirection(cols []SortColumn) bool {
	if len(cols) <= 1 {
		return true
	}
	dir := cols[0].Direction
	for _, c := range cols[1:] {
		if c.Direction != dir {
			return false
		}
	}
	return true
}

// directionOp returns ">" for ASC, "<" for DESC.
func directionOp(dir SortDir) string {
	if dir == SortDESC {
		return "<"
	}
	return ">"
}

// appendKeysetWhere generates the keyset WHERE clause.
func appendKeysetWhere(b *Builder, cols []SortColumn, values []any) error {
	if len(cols) == 1 {
		op := directionOp(cols[0].Direction)
		b.AppendParam(fmt.Sprintf("AND %s %s ", cols[0].Name, op), values[0])
		return nil
	}

	if sameDirection(cols) {
		return appendTupleComparison(b, cols, values)
	}
	return appendCompoundOR(b, cols, values)
}

// appendTupleComparison generates: AND (col1, col2) > ($1, $2)
func appendTupleComparison(b *Builder, cols []SortColumn, values []any) error {
	op := directionOp(cols[0].Direction)

	names := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
		placeholders[i] = b.NextParam(values[i])
	}

	clause := fmt.Sprintf("AND (%s) %s (%s)",
		strings.Join(names, ", "),
		op,
		strings.Join(placeholders, ", "))
	b.Append(clause)
	return nil
}

// appendCompoundOR generates: AND (col1 < $1 OR (col1 = $2 AND col2 > $3) OR ...)
func appendCompoundOR(b *Builder, cols []SortColumn, values []any) error {
	var parts []string

	for level := 0; level < len(cols); level++ {
		var conditions []string

		for j := 0; j < level; j++ {
			p := b.NextParam(values[j])
			conditions = append(conditions, fmt.Sprintf("%s = %s", cols[j].Name, p))
		}

		op := directionOp(cols[level].Direction)
		p := b.NextParam(values[level])
		conditions = append(conditions, fmt.Sprintf("%s %s %s", cols[level].Name, op, p))

		if len(conditions) == 1 {
			parts = append(parts, conditions[0])
		} else {
			parts = append(parts, "("+strings.Join(conditions, " AND ")+")")
		}
	}

	b.Append("AND (" + strings.Join(parts, " OR ") + ")")
	return nil
}

// appendOrderBy generates: ORDER BY col1 DIR1, col2 DIR2
func appendOrderBy(b *Builder, cols []SortColumn) {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c.Name + " " + string(c.Direction)
	}
	b.Append("ORDER BY " + strings.Join(parts, ", "))
}
