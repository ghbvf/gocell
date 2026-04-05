package postgres

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RowScanner abstracts a single-row scan target, satisfied by both pgx.Row
// and *pgx.Rows. Repository implementations use this to write scan logic
// that works for both QueryRow and Query result sets.
type RowScanner interface {
	// Scan reads column values into dest, analogous to pgx.Row.Scan.
	Scan(dest ...any) error
}

// Ensure pgx.Row satisfies RowScanner at compile time.
// pgx.Rows also satisfies RowScanner (via its Scan method) but is itself an
// interface, so a compile-time assertion is omitted.
var _ RowScanner = (pgx.Row)(nil)

// QueryBuilder assists in constructing parameterized SQL queries.
// It tracks positional parameters ($1, $2, ...) and their values.
type QueryBuilder struct {
	parts []string
	args  []any
}

// NewQueryBuilder creates an empty QueryBuilder.
func NewQueryBuilder() *QueryBuilder {
	return &QueryBuilder{}
}

// Append adds a raw SQL fragment to the query.
func (qb *QueryBuilder) Append(sql string) *QueryBuilder {
	qb.parts = append(qb.parts, sql)
	return qb
}

// AppendParam adds a SQL fragment containing a single positional parameter and
// its value. The placeholder $N is inserted automatically.
func (qb *QueryBuilder) AppendParam(sqlPrefix string, value any) *QueryBuilder {
	qb.args = append(qb.args, value)
	placeholder := fmt.Sprintf("$%d", len(qb.args))
	qb.parts = append(qb.parts, sqlPrefix+placeholder)
	return qb
}

// AppendIf conditionally appends a parameterized clause.
// This is useful for optional WHERE conditions.
func (qb *QueryBuilder) AppendIf(condition bool, sqlPrefix string, value any) *QueryBuilder {
	if condition {
		return qb.AppendParam(sqlPrefix, value)
	}
	return qb
}

// Build returns the assembled SQL string and the ordered argument slice.
func (qb *QueryBuilder) Build() (string, []any) {
	return strings.Join(qb.parts, " "), qb.args
}

// Args returns the current argument slice.
func (qb *QueryBuilder) Args() []any {
	return qb.args
}

// SQL returns the current query string without arguments.
func (qb *QueryBuilder) SQL() string {
	return strings.Join(qb.parts, " ")
}

// NextParam returns the next positional placeholder string (e.g. "$3")
// without adding any arguments. Useful when building complex clauses manually.
func (qb *QueryBuilder) NextParam(value any) string {
	qb.args = append(qb.args, value)
	return fmt.Sprintf("$%d", len(qb.args))
}

// Reset clears the builder for reuse.
func (qb *QueryBuilder) Reset() *QueryBuilder {
	qb.parts = qb.parts[:0]
	qb.args = qb.args[:0]
	return qb
}
