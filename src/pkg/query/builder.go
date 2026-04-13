// Package query provides utilities for constructing parameterized SQL queries.
package query

import (
	"strconv"
	"strings"
)

// Builder assists in constructing parameterized SQL queries.
// It tracks positional parameters ($1, $2, ...) and their values.
type Builder struct {
	parts []string
	args  []any
}

// NewBuilder creates an empty Builder.
func NewBuilder() *Builder {
	// Optimization: Pre-allocate slices with a small default capacity
	// to avoid multiple reallocations during typical query construction.
	return &Builder{
		parts: make([]string, 0, 8),
		args:  make([]any, 0, 8),
	}
}

// Append adds a raw SQL fragment to the query.
func (b *Builder) Append(sql string) *Builder {
	b.parts = append(b.parts, sql)
	return b
}

// AppendParam adds a SQL fragment containing a single positional parameter and
// its value. The placeholder $N is inserted automatically.
func (b *Builder) AppendParam(sqlPrefix string, value any) *Builder {
	b.args = append(b.args, value)
	// Optimization: Use strconv.Itoa instead of fmt.Sprintf to avoid reflection
	// and unnecessary heap allocations, improving string formatting performance.
	placeholder := "$" + strconv.Itoa(len(b.args))
	b.parts = append(b.parts, sqlPrefix+placeholder)
	return b
}

// AppendIf conditionally appends a parameterized clause.
// This is useful for optional WHERE conditions.
func (b *Builder) AppendIf(condition bool, sqlPrefix string, value any) *Builder {
	if condition {
		return b.AppendParam(sqlPrefix, value)
	}
	return b
}

// Build returns the assembled SQL string and a copy of the argument slice.
// The returned args are safe to hold across Reset() calls.
func (b *Builder) Build() (string, []any) {
	return strings.Join(b.parts, " "), append([]any(nil), b.args...)
}

// Args returns a copy of the current argument slice.
// The returned slice is safe to hold across Reset() calls.
func (b *Builder) Args() []any {
	return append([]any(nil), b.args...)
}

// SQL returns the current query string without arguments.
func (b *Builder) SQL() string {
	return strings.Join(b.parts, " ")
}

// NextParam registers value as the next positional argument and returns
// its placeholder string (e.g. "$3"). Useful when building complex clauses
// where AppendParam's single-prefix pattern is insufficient.
func (b *Builder) NextParam(value any) string {
	b.args = append(b.args, value)
	// Optimization: Use strconv.Itoa instead of fmt.Sprintf to avoid reflection
	// and unnecessary heap allocations, improving string formatting performance.
	return "$" + strconv.Itoa(len(b.args))
}

// Reset clears the builder for reuse.
func (b *Builder) Reset() *Builder {
	b.parts = b.parts[:0]
	b.args = b.args[:0]
	return b
}
